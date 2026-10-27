"""Boundary checks and opt-in real restores of wholly invented archives."""

import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("synthetic_restore_fixture", HERE / "synthetic_bundle.py")
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


def private_directory(parent, name):
    path = parent / name
    path.mkdir(mode=0o700)
    return path


def reseal(bundle):
    """Update fixture checksums to test archive validity beyond hash matching."""
    manifest = json.loads((bundle / "manifest.json").read_text())
    for name in ("database.dump", "globals.sql", "toc.list"):
        content = (bundle / name).read_bytes()
        manifest["files"][name] = {"bytes": len(content), "sha256": hashlib.sha256(content).hexdigest()}
    (bundle / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (bundle / "SHA256SUMS").write_text("".join(f'{value["sha256"]}  {name}\n' for name, value in manifest["files"].items()))
    for path in bundle.iterdir():
        if path.is_file():
            path.chmod(0o600)


class FixtureBoundaryTests(unittest.TestCase):
    def test_default_never_runs_a_command(self):
        with mock.patch.object(FIXTURE.BACKUP.Runner, "run") as command, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(FIXTURE.main([]), 0)
        command.assert_not_called()
        self.assertEqual(json.loads(output.getvalue()), {"status": "tools_only", "synthetic_only": True, "action": "none"})

    def test_fixture_directory_refuses_existing_or_git_content_before_docker(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw).resolve()
            existing = private_directory(root, "existing")
            (existing / "keep").write_text("operator-data-must-remain")
            git = private_directory(root, "checkout")
            (git / ".git").mkdir()
            inside = private_directory(git, "empty")
            for directory in (existing, inside):
                with self.subTest(directory=directory), mock.patch.object(FIXTURE.BACKUP.Runner, "run") as command:
                    with self.assertRaises((FIXTURE.SyntheticError, FIXTURE.BACKUP.BackupError)):
                        FIXTURE.create_bundle(directory)
                    command.assert_not_called()
            self.assertEqual((existing / "keep").read_text(), "operator-data-must-remain")

    def test_arbitrary_socket_and_variant_are_refused(self):
        with self.assertRaises(FIXTURE.SyntheticError):
            FIXTURE.approved_socket("/tmp/not-an-approved-daemon.sock")
        with self.assertRaises(FIXTURE.SyntheticError):
            FIXTURE.create_bundle("/unused", variant="real-data")


@unittest.skipUnless(os.environ.get("RUN_SYNTHETIC_RESTORE_TESTS") == "1", "synthetic Docker restore integration is opt-in")
class SyntheticArchiveRestoreTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        provided = os.environ.get("SYNTHETIC_RESTORE_PRIVATE_OUTPUT")
        if provided:
            cls.root = FIXTURE.private_empty_directory(provided)
        else:
            cls.temporary = tempfile.TemporaryDirectory(prefix="gnomon-restore-e2e-")
            cls.root = Path(cls.temporary.name).resolve()
            cls.root.chmod(0o700)
            cls.addClassCleanup(cls.temporary.cleanup)
        cls.bundle = FIXTURE.create_bundle(private_directory(cls.root, "source-valid"))

    def run_restore(self, bundle, name, *, initdb=False, expected=0, expected_error=None, timeout_seconds=120):
        output = private_directory(self.root, "restore-" + name)
        arguments = [sys.executable, "-B", str(HERE / "runner.py"), "--execute-restore",
                     "--bundle", str(bundle), "--private-output-dir", str(output), "--timeout-seconds", str(timeout_seconds)]
        if initdb:
            arguments.append("--run-initdb")
        environment = {key: value for key, value in os.environ.items() if not key.startswith("DOCKER_") and not key.startswith("BUILDX_") and key != "BUILDKIT_HOST"}
        result = subprocess.run(arguments, capture_output=True, timeout=1200, env=environment)
        for stream, content in (("stdout", result.stdout), ("stderr", result.stderr)):
            path = output / ("cli-" + stream + ".private")
            path.write_bytes(content)
            path.chmod(0o600)
        # A failing assertion gives no SQL, archive payload, endpoint or DSN.
        self.assertEqual(result.returncode, expected, "restore CLI result differed; diagnostics remain in private output")
        for payload in (b"synthetic-validator", b"empty-chain", b"restore_globals_canary"):
            self.assertNotIn(payload, result.stdout, "private fixture payload reached public stdout")
            self.assertNotIn(payload, result.stderr, "private fixture payload reached public stderr")
        messages = [json.loads(line) for line in result.stdout.decode().splitlines() if line.strip()]
        self.assertFalse(any(item.get("status") == "restore_cleanup_failed" for item in messages), "owned resource cleanup failed")
        frozen = output / "resolved-compose.private.json"
        if frozen.is_file():
            project = json.loads(frozen.read_text())["name"]
            self.assertTrue(project.startswith("gnomon-restore-"))
            docker = ["docker", "--config", str(output / "docker-config"), "--host", "unix://" + FIXTURE.approved_socket()]
            selectors = [
                ["ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project=" + project],
                ["volume", "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project],
                ["image", "ls", "--quiet", "--filter", "reference=" + project + "-*:local-only"],
            ]
            for selector in selectors:
                inspected = subprocess.run(docker + selector, capture_output=True, timeout=30, env=environment)
                self.assertEqual(inspected.returncode, 0, "owned resource inspection failed")
                self.assertFalse(inspected.stdout.strip(), "restore left an owned runtime resource")
        summary_path = output / "restore-summary.json"
        if expected == 1:
            self.assertFalse(summary_path.exists(), "failed restore gained a completed summary")
            self.assertFalse(any(item.get("restore_verified") for item in messages), "failed restore claimed verification")
            failure = next(item for item in reversed(messages) if item.get("status") == "restore_refused_or_failed")
            if expected_error:
                self.assertRegex(failure.get("error_class", ""), expected_error, "restore failed before the case being tested")
            return output, None
        self.assertTrue(summary_path.is_file(), "successful restore has no private summary")
        self.assertEqual(stat.S_IMODE(summary_path.stat().st_mode), 0o600)
        summary = json.loads(summary_path.read_text())
        self.assertTrue(summary["restore_verified"])
        self.assertFalse(summary["source_snapshot_equality_verified"])
        self.assertFalse(summary["globals_applied"])
        self.assertEqual(summary["network_mode"], "none")
        self.assertTrue(summary["owned_resources_cleanup_verified"])
        self.assertTrue(summary["marker_sealed"])
        return output, summary

    @staticmethod
    def scopes(table):
        return {(item["scope"], item.get("chain_id")): item["rows"] for item in table["scopes"]}

    def check_baseline(self, summary):
        baseline = summary["baseline"]
        tables = {entry["name"]: entry for entry in baseline["tables"] if entry["schema"] == "public"}
        self.assertEqual(tables["archive_probe"]["rows"], 5)
        self.assertEqual(self.scopes(tables["archive_probe"]), {
            ("chain", "synthetic-chain"): 2,
            ("chain", "synthetic-peer-chain"): 1,
            ("chain", ""): 1,
            ("null_chain_id", None): 1,
        })
        self.assertEqual(tables["daily_participations"]["rows"], 3)
        self.assertEqual(self.scopes(tables["daily_participations"]), {
            ("chain", "synthetic-chain"): 2, ("chain", "synthetic-peer-chain"): 1,
        })
        # A relation outside the eight InitDB count tables must be validated,
        # including its constraints, index, view and sequence state.
        self.assertIn("archive_probe_amount_positive", json.dumps(tables["archive_probe"]["constraints"]))
        self.assertIn("archive_probe_chain_marker_key", json.dumps(tables["archive_probe"]["constraints"]))
        self.assertIn("archive_probe_chain_idx", json.dumps(tables["archive_probe"]["indexes"]))
        self.assertIn("archive_probe_chain_totals", json.dumps(baseline["views"]))
        sequence = next(item for item in baseline["sequences"] if item["schema"] == "public" and item["name"] == "archive_probe_id_seq")
        self.assertEqual(sequence["last_value"], 5)
        self.assertTrue(sequence["is_called"])
        self.assertNotIn("restore_globals_canary", json.dumps(baseline["roles"]))
        self.assertNotIn("fixture_owner", json.dumps(baseline["roles"]))
        self.assertTrue(all(entry["owner"] == "rehearsal_owner" for entry in tables.values()))
        marker = summary["marker_state"]
        self.assertEqual(marker["schema_owner"], "rehearsal_admin")
        self.assertEqual(marker["table_owner"], "rehearsal_admin")
        self.assertFalse(marker["app_can_write"])
        self.assertFalse(marker["app_can_create"])
        self.assertEqual(marker["dsn_mode"], "0600")

    def test_custom_archive_restores_all_fixture_objects_without_globals(self):
        self.assertIn("restore_globals_canary", (self.bundle / "globals.sql").read_text())
        _, summary = self.run_restore(self.bundle, "complete")
        self.check_baseline(summary)
        self.assertIsNone(summary["initdb"])
        self.assertIsNone(summary["initdb_provenance"])

    def test_initdb_is_a_separate_review_phase_after_verified_restore(self):
        _, summary = self.run_restore(self.bundle, "initdb", initdb=True, expected=3)
        self.check_baseline(summary)
        helper_names = ("main.go", "guard.go", "guard_test.go", "fd_linux.go", "fd_darwin.go")
        expected_provenance = {}
        for name in helper_names:
            relative = "backend/cmd/rehearse-initdb/" + name
            expected_provenance[relative] = hashlib.sha256((FIXTURE.ROOT / relative).read_bytes()).hexdigest()
        self.assertEqual(summary["initdb_provenance"], expected_provenance)
        phase = summary["initdb"]
        first = phase["first_pass"]
        second = phase["second_pass"]
        self.assertEqual(first["status"], "completed_requires_deletion_review")
        self.assertEqual(first["rows_removed"], {"daily_participations": 1})
        self.assertEqual(first["scope_reductions"], [{
            "table": "daily_participations", "scope": "chain", "chain_id": "synthetic-chain",
            "before": 2, "after": 1, "rows_reduced": 1,
        }])
        self.assertEqual(second["status"], "completed")
        self.assertFalse(second.get("rows_removed"))
        self.assertFalse(second.get("scope_reductions"))
        self.assertEqual(second["before"], second["after"])

    def test_bad_hash_is_refused_without_completed_restore(self):
        broken = self.root / "bad-hash"
        shutil.copytree(self.bundle, broken)
        with (broken / "database.dump").open("ab") as archive:
            archive.write(b"synthetic-corruption")
        self.run_restore(broken, "bad-hash", expected=1, expected_error="^bundle_size_or_hash_mismatch$")

    def test_truncated_archive_with_consistent_hashes_is_not_promoted(self):
        broken = self.root / "truncated"
        shutil.copytree(self.bundle, broken)
        path = broken / "database.dump"
        content = path.read_bytes()
        path.write_bytes(content[: max(5, len(content) // 2)])
        reseal(broken)
        self.run_restore(broken, "truncated", expected=1, expected_error="^restore_step_failed_(verify_actual_archive_toc|restore_nonprivileged_transaction)_exit_[1-9][0-9]*$")

    def test_reserved_marker_archive_is_refused(self):
        bundle = FIXTURE.create_bundle(private_directory(self.root, "source-reserved"), variant="reserved-marker")
        self.run_restore(bundle, "reserved-marker", expected=1, expected_error="^reserved_guard_schema_in_archive$")

    def test_restore_role_does_not_gain_admin_privilege_for_archive(self):
        bundle = FIXTURE.create_bundle(private_directory(self.root, "source-restore-error"), variant="restore-error")
        output, _ = self.run_restore(bundle, "privileged-function", expected=1,
                                     expected_error="^restore_step_failed_restore_nonprivileged_transaction_exit_1$")
        diagnostics = b"\n".join(path.read_bytes() for path in output.glob("*-stderr.private"))
        self.assertTrue(b"permission denied for language internal" in diagnostics or b"must be superuser" in diagnostics,
                        "privileged-function test did not reach the expected SQL permission refusal")

    def test_restore_timeout_cannot_gain_marker_or_completed_report(self):
        bundle = FIXTURE.create_bundle(private_directory(self.root, "source-restore-timeout"), variant="restore-timeout")
        self.run_restore(bundle, "timeout", expected=1, timeout_seconds=30,
                         expected_error="^restore_step_failed_restore_nonprivileged_transaction_exit_124$")


if __name__ == "__main__":
    unittest.main()
