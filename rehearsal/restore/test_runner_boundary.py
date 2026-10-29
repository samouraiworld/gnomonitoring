"""Fail-closed restore boundaries; no Docker or database is required."""

import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("restore_runner", HERE / "runner.py")
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


class RestoreBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="restore-boundaries-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.bundle = self.root / "bundle"
        self.bundle.mkdir(mode=0o700)
        self.output = self.root / "output"
        self.output.mkdir(mode=0o700)
        payloads = {"database.dump": b"PGDMPsynthetic-boundary-only", "globals.sql": b"-- never executed\n",
                    "toc.list": b"; synthetic boundary only\n1; 0 0 TABLE public fixture fixture_owner\n"}
        self.manifest = {"format_version": 1, "kind": "gnomonitoring-postgres-backup", "complete": True,
                         "restore_verified": False, "source": {"postgres_major": 16, "database_bytes": 4096},
                         "files": {name: {"bytes": len(value), "sha256": hashlib.sha256(value).hexdigest()}
                                   for name, value in payloads.items()}}
        for name, value in payloads.items():
            self.write(name, value)
        self.write("SHA256SUMS", "".join(f'{value["sha256"]}  {name}\n' for name, value in self.manifest["files"].items()).encode())
        self.save_manifest()

    def write(self, name, content):
        path = self.bundle / name
        path.write_bytes(content)
        path.chmod(0o600)

    def save_manifest(self):
        self.write("manifest.json", json.dumps(self.manifest).encode())

    def reseal(self, name, content):
        self.write(name, content)
        self.manifest["files"][name] = {"bytes": len(content), "sha256": hashlib.sha256(content).hexdigest()}
        self.save_manifest()
        self.write("SHA256SUMS", "".join(f'{value["sha256"]}  {key}\n' for key, value in self.manifest["files"].items()).encode())

    def bare_runner(self):
        runner = RUNNER.RestoreRunner.__new__(RUNNER.RestoreRunner)
        runner.output = self.output
        runner.environment = {"PATH": os.environ["PATH"]}
        runner.sequence = 0
        runner.stage = "unit_boundary"
        runner.docker = ["docker", "--host", "unix:///approved/local.sock"]
        return runner

    def test_no_execution_without_both_explicit_opt_ins(self):
        with mock.patch.object(RUNNER, "RestoreRunner") as constructor, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(RUNNER.main(["--run-initdb"]), 0)
        constructor.assert_not_called()
        self.assertEqual(json.loads(output.getvalue()), {"status": "tools_only", "action": "none"})

    def test_valid_manifest_checks_files_without_docker(self):
        with mock.patch.object(RUNNER.subprocess, "Popen") as command:
            self.assertEqual(RUNNER.validate_bundle(self.bundle), self.manifest)
        command.assert_not_called()

    def test_incomplete_wrong_major_or_noninteger_version_is_refused(self):
        for name, value in [("complete", False), ("format_version", True), ("kind", "other")]:
            with self.subTest(field=name):
                original = self.manifest[name]
                self.manifest[name] = value
                self.save_manifest()
                with self.assertRaises(RUNNER.Refused):
                    RUNNER.validate_bundle(self.bundle)
                self.manifest[name] = original
        for major in (15, "16", 16.0, True):
            self.manifest["source"]["postgres_major"] = major
            self.save_manifest()
            with self.assertRaises(RUNNER.Refused):
                RUNNER.validate_bundle(self.bundle)

    def test_changed_archive_even_same_length_is_refused(self):
        path = self.bundle / "database.dump"
        value = path.read_bytes()
        self.write("database.dump", value[:-1] + b"X")
        with self.assertRaisesRegex(RUNNER.Refused, "bundle_size_or_hash_mismatch"):
            RUNNER.validate_bundle(self.bundle)

    def test_symlink_and_public_bundle_members_are_refused(self):
        archive = self.bundle / "database.dump"
        original = archive.read_bytes()
        archive.chmod(0o644)
        with self.assertRaises(RUNNER.Refused):
            RUNNER.validate_bundle(self.bundle)
        archive.unlink()
        outside = self.root / "outside"
        outside.write_bytes(original)
        outside.chmod(0o600)
        archive.symlink_to(outside)
        with self.assertRaises((RUNNER.Refused, OSError)):
            RUNNER.validate_bundle(self.bundle)

    def test_fifo_is_refused_without_waiting_for_a_writer(self):
        path = self.bundle / "database.dump"
        path.unlink()
        os.mkfifo(path, 0o600)
        with mock.patch.object(RUNNER.subprocess, "Popen") as command:
            with self.assertRaises(RUNNER.Refused):
                RUNNER.validate_bundle(self.bundle)
        command.assert_not_called()

    def test_private_directory_rejects_git_symlink_and_nonempty_output(self):
        (self.output / "operator-file").write_text("keep")
        with self.assertRaises(RUNNER.Refused):
            RUNNER.private_directory(self.output, empty=True)
        checkout = self.root / "checkout"
        checkout.mkdir(mode=0o700)
        (checkout / ".git").mkdir()
        with self.assertRaises(RUNNER.Refused):
            RUNNER.private_directory(checkout)
        link = self.root / "link"
        link.symlink_to(self.bundle)
        with self.assertRaises(RUNNER.Refused):
            RUNNER.private_directory(link)
        self.assertEqual((self.output / "operator-file").read_text(), "keep")

    def test_checksum_list_duplicate_json_and_reserved_schema_are_refused(self):
        self.write("SHA256SUMS", b"0" * 64 + b"  database.dump\n")
        with self.assertRaises(RUNNER.Refused):
            RUNNER.validate_bundle(self.bundle)
        self.write("manifest.json", b'{"complete":true,"complete":false}')
        with self.assertRaisesRegex(RUNNER.Refused, "duplicate_json_key"):
            RUNNER.validate_bundle(self.bundle)
        self.reseal("toc.list", b"1; 0 0 SCHEMA - rehearsal_guard fixture_owner\n")
        with self.assertRaisesRegex(RUNNER.Refused, "reserved_guard_schema_in_archive"):
            RUNNER.validate_bundle(self.bundle)

    def test_noncustom_archive_cannot_be_validated_by_matching_hashes(self):
        self.reseal("database.dump", b"-- plain SQL is not an accepted archive\n")
        with self.assertRaisesRegex(RUNNER.Refused, "custom_archive_required"):
            RUNNER.validate_bundle(self.bundle)

    def test_actual_mount_destination_and_readonly_socket_are_checked(self):
        project = "gnomon-restore-fixture"
        safe = {"network_mode": "none", "ports": {}, "privileged": False, "pid_mode": "", "user": "999:999",
                "cap_drop": ["ALL"], "cap_add": [], "security_opt": ["no-new-privileges:true"], "project": project,
                "mounts": [{"Type": "volume", "Name": project + "-pgdata", "Destination": "/var/lib/postgresql/data", "RW": True},
                           {"Type": "volume", "Name": project + "-socket", "Destination": RUNNER.SOCKET, "RW": True},
                           {"Type": "volume", "Name": project + "-private-logs", "Destination": "/run/rehearsal-private", "RW": True}]}
        RUNNER.validate_actual_container(safe, project, "postgres")
        for change in [{"project": "unrelated"}, {"network_mode": "host"}, {"cap_add": ["SYS_ADMIN"]}]:
            with self.assertRaises(RUNNER.Refused):
                RUNNER.validate_actual_container({**safe, **change}, project, "postgres")
        altered = json.loads(json.dumps(safe))
        altered["mounts"][0]["Destination"] = "/unexpected"
        with self.assertRaises(RUNNER.Refused):
            RUNNER.validate_actual_container(altered, project, "postgres")

    def test_stderr_warning_exit_zero_is_private_and_fatal(self):
        runner = self.bare_runner()
        with self.assertRaisesRegex(RUNNER.Refused, "restore_step_failed_unit_boundary_exit_0"):
            runner.command([sys.executable, "-c", "import sys; sys.stderr.write('private-sql-detail')"], reject_stderr=True)
        path = self.output / "001-stderr.private"
        self.assertEqual(path.read_text(), "private-sql-detail")
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_diagnostic_overflow_is_bounded_and_fatal(self):
        runner = self.bare_runner()
        with self.assertRaises(RUNNER.Refused):
            runner.command([sys.executable, "-c", f"import sys; sys.stderr.write('x'*{RUNNER.LOG_LIMIT+4096})"])
        self.assertEqual((self.output / "001-stderr.private").stat().st_size, RUNNER.LOG_LIMIT)

    def test_timeout_closes_child_pipes(self):
        runner = self.bare_runner()
        with self.assertRaises(subprocess.TimeoutExpired):
            runner.command([sys.executable, "-c", "import time; time.sleep(30)"], timeout=0.1)

    def test_restore_timeout_is_inside_container_and_globals_never_selected(self):
        runner = self.bare_runner()
        runner.postgres_id = "a" * 64
        with mock.patch.object(runner, "command", return_value=("", 0)) as command:
            runner.exec_pg(["pg_restore", "--no-owner", "--single-transaction"], timeout=123)
        arguments = command.call_args.args[0]
        self.assertEqual(arguments[:3], runner.docker)
        self.assertIn("timeout", arguments)
        self.assertIn("123s", arguments)
        self.assertIn("PGOPTIONS=-c statement_timeout=123000 -c lock_timeout=5000 -c search_path=pg_catalog -c standard_conforming_strings=on", arguments)
        self.assertNotIn("globals.sql", arguments)
        self.assertEqual(command.call_args.kwargs["timeout"], 138)
        self.assertTrue(command.call_args.kwargs["reject_stderr"])

    def test_analyze_sql_and_process_use_the_configured_budget(self):
        runner = self.bare_runner()
        runner.postgres_id = "a" * 64
        runner.database = "gnomonitoring_rehearsal_" + "a" * 32
        with mock.patch.object(runner, "command", return_value=("", 0)) as command:
            runner.sql('ANALYZE "public"."fixture"', timeout=90)
        arguments = command.call_args.args[0]
        self.assertIn("PGOPTIONS=-c statement_timeout=90000 -c lock_timeout=5000 -c search_path=pg_catalog -c standard_conforming_strings=on", arguments)
        self.assertIn("90s", arguments)
        self.assertEqual(command.call_args.kwargs["timeout"], 105)

    def test_completion_is_not_left_after_fsync_failure(self):
        for failure in ("file", "directory"):
            with self.subTest(failure=failure):
                target = RUNNER.os if failure == "file" else RUNNER
                name = "fsync" if failure == "file" else "sync_directory"
                with mock.patch.object(target, name, side_effect=OSError("injected")):
                    with self.assertRaises(OSError):
                        RUNNER.publish_summary(self.output, {"restore_verified": True})
                self.assertFalse((self.output / "restore-summary.json").exists())
                self.assertFalse((self.output / ".restore-summary.pending").exists())

    def test_failure_after_summary_rename_removes_completion_marker(self):
        with mock.patch.object(RUNNER, "sync_directory", side_effect=[None, OSError("injected post-rename failure")]):
            with self.assertRaises(OSError):
                RUNNER.publish_summary(self.output, {"restore_verified": True})
        self.assertFalse((self.output / "restore-summary.json").exists())

    def test_restore_failure_cannot_seal_marker_or_be_masked_by_cleanup(self):
        runner = self.bare_runner()
        runner.bundle = self.bundle
        runner.project = "gnomon-restore-boundary"
        runner.clone_id = "a" * 32
        runner.database = "gnomonitoring_rehearsal_" + runner.clone_id
        runner.timeout_seconds = 30
        runner.compose = runner.docker + ["compose", "--file", "/source/compose.restore.yml"]
        config = {"services": {}, "volumes": {key: {"name": runner.project + "-" + suffix}
                  for key, suffix in [("pgdata", "pgdata"), ("socket", "socket"), ("private_logs", "private-logs")]}}
        for name in ("postgres", "initdb"):
            service = {"network_mode": "none", "user": "999:999", "cap_drop": ["ALL"],
                       "image": runner.project + "-" + name + ":local-only", "volumes": [
                           {"type": "volume", "source": "socket", "target": RUNNER.SOCKET, "read_only": name == "initdb"},
                           {"type": "volume", "source": "private_logs", "target": "/run/rehearsal-private"}]}
            if name == "postgres":
                service["environment"] = {"POSTGRES_USER": "rehearsal_admin", "POSTGRES_DB": "postgres",
                                          "POSTGRES_PASSWORD": "synthetic-only", "REHEARSAL_CLONE_ID": runner.clone_id}
                service["volumes"].append({"type": "volume", "source": "pgdata", "target": "/var/lib/postgresql/data"})
            else:
                service["read_only"] = True
            config["services"][name] = service
        original_compose = runner.compose[:]
        commands = []
        def command(arguments, **unused):
            commands.append(arguments)
            if arguments == runner.docker + ["info", "--format", "{{.OSType}}"]:
                return "linux", 0
            if arguments == original_compose + ["config", "--format", "json"]:
                return json.dumps(config), 0
            if arguments[-4:] == ["ps", "--all", "--quiet", "postgres"]:
                return "b" * 64, 0
            return "", 0
        def archive_command(arguments, **unused):
            if arguments == ["pg_restore", "--list"]:
                return (self.bundle / "toc.list").read_text()
            raise RUNNER.Refused("original_restore_failure")
        with mock.patch.object(runner, "command", side_effect=command), \
                mock.patch.object(runner, "fresh_volumes"), mock.patch.object(runner, "inspect_volume_fence"), \
                mock.patch.object(runner, "inspect_container"), mock.patch.object(runner, "wait_postgres"), \
                mock.patch.object(runner, "disk_free", return_value=100 * RUNNER.GIB), \
                mock.patch.object(runner, "sql", return_value="160015"), \
                mock.patch.object(runner, "exec_pg", side_effect=archive_command), \
                mock.patch.object(runner, "seal_marker") as marker, \
                mock.patch.object(runner, "cleanup", side_effect=RUNNER.Refused("secondary_cleanup_failure")), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            with self.assertRaisesRegex(RUNNER.Refused, "^original_restore_failure$"):
                runner.execute()
        marker.assert_not_called()
        self.assertEqual(json.loads(output.getvalue()), {"status": "restore_cleanup_failed"})
        self.assertFalse((self.output / "restore-summary.json").exists())
        frozen = self.output / "resolved-compose.private.json"
        self.assertEqual(json.loads(frozen.read_text()), config)
        self.assertTrue(all(str(frozen) in arguments for arguments in commands if "build" in arguments or "create" in arguments))


if __name__ == "__main__":
    unittest.main()
