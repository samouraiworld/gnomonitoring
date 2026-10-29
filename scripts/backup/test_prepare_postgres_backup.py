#!/usr/bin/env python3
"""Local fixtures only; the Docker integration never uses a production target."""

import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("backup", Path(__file__).with_name("prepare-postgres-backup.py"))
backup = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(backup)


class FixtureRunner:
    def __init__(self, directory=None):
        self.calls = []

    def run(self, command, **kwargs):
        self.calls.append(command)
        output = kwargs.get("output")
        if "inspect" in command:
            return json.dumps({"id": "a" * 64, "image": "sha256:" + "b" * 64, "status": "running", "project": "synthetic"}).encode()
        if "--version" in command:
            return f"{command[-2]} (PostgreSQL) 16.11\n".encode()
        if "psql" in command:
            return json.dumps({"database": "backup_fixture", "user": "backup_test", "server_version_num": 160011,
                               "database_bytes": 8192, "transaction_read_only": "on"}).encode()
        if "pg_dump" in command:
            output.write(b"PGDMPsynthetic-custom-archive")
        elif "pg_dumpall" in command:
            output.write(b"-- Synthetic globals only\nCREATE ROLE backup_test;\n")
        elif "pg_restore" in command:
            if kwargs["input_file"].read(5) != b"PGDMP":
                raise backup.BackupError("fixture input is not custom format")
            output.write(b"; Synthetic table of contents\n1; 0 0 TABLE public backup_canary backup_test\n")
        else:
            raise AssertionError("unexpected command")
        return b""


class BackupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="bk-")
        self.addCleanup(self.temp.cleanup)
        parent = Path(self.temp.name).resolve()
        self.destination = parent / "output"
        self.destination.mkdir(mode=0o700)
        self.socket_path = str(parent / "docker.sock")
        self.socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.addCleanup(self.socket.close)
        self.socket.bind(self.socket_path)
        self.args = argparse.Namespace(container_id="a" * 64, expected_image_id="sha256:" + "b" * 64,
            docker_socket=self.socket_path,
            compose_project="synthetic", database="backup_fixture", user="backup_test", postgres_major=16,
            socket_directory="/var/run/postgresql", port=5432,
            destination=str(self.destination), dump_timeout_seconds=30)

    def test_default_mode_checks_without_exporting(self):
        args = ["--container-id", self.args.container_id, "--expected-image-id", self.args.expected_image_id,
            "--compose-project", "synthetic", "--database", "backup_fixture", "--user", "backup_test",
            "--postgres-major", "16", "--destination", str(self.destination)]
        args.extend(["--socket-directory", "/var/run/postgresql", "--port", "5432"])
        args.extend(["--docker-socket", self.socket_path])
        output = io.StringIO()
        with patch.object(backup, "Runner", FixtureRunner), patch.object(backup, "execute") as execute, contextlib.redirect_stdout(output):
            self.assertEqual(backup.main(args), 0)
        execute.assert_not_called()
        self.assertIn("no export performed", output.getvalue())
        self.assertNotIn("backup_fixture", output.getvalue())
        self.assertNotIn(self.args.expected_image_id, output.getvalue())
        self.assertEqual(list(self.destination.iterdir()), [])

    def test_private_canonical_existing_destination_required(self):
        for value in ["relative", str(self.destination / "missing"), str(self.destination / ".." / self.destination.name)]:
            with self.subTest(value=value), self.assertRaises(backup.BackupError):
                backup.validate_destination(value)
        link = self.destination / "link"
        link.symlink_to(self.destination, target_is_directory=True)
        with self.assertRaises(backup.BackupError):
            backup.validate_destination(str(link))
        self.destination.chmod(0o750)
        with self.assertRaises(backup.BackupError):
            backup.validate_destination(str(self.destination))

    def test_git_checkout_destination_rejected(self):
        (self.destination / ".git").mkdir()
        with self.assertRaises(backup.BackupError):
            backup.validate_destination(str(self.destination))

    def test_untrusted_writable_parent_or_wrong_owner_rejected(self):
        child = self.destination / "child"
        child.mkdir(mode=0o700)
        self.destination.chmod(0o777)
        with self.assertRaises(backup.BackupError):
            backup.validate_destination(str(child))
        self.destination.chmod(0o700)
        with patch.object(backup.os, "geteuid", return_value=os.geteuid() + 1), self.assertRaises(backup.BackupError):
            backup.validate_destination(str(child))

    def test_target_identity_and_major_fail_closed(self):
        for attribute, value in [("container_id", "short"), ("expected_image_id", "latest"),
                                 ("compose_project", "wrong-project"), ("database", "wrong_database"),
                                 ("postgres_major", 17), ("database", "-option"), ("port", 0),
                                 ("socket_directory", "/var/run/postgresql,unapproved-server"),
                                 ("socket_directory", "/tmp/../var/run/postgresql")]:
            original = getattr(self.args, attribute)
            setattr(self.args, attribute, value)
            with self.subTest(attribute=attribute), self.assertRaises(backup.BackupError):
                backup.check(self.args, FixtureRunner())
            setattr(self.args, attribute, original)

    def test_free_space_failure_precedes_export(self):
        with patch.object(backup.shutil, "disk_usage", return_value=shutil._ntuple_diskusage(10, 9, 1)):
            with self.assertRaises(backup.BackupError):
                backup.check(self.args, FixtureRunner())
        self.assertEqual(list(self.destination.iterdir()), [])

    def test_stderr_is_private_bounded_and_not_exposed(self):
        runner = backup.Runner(self.destination)
        with self.assertRaises(backup.BackupError) as caught:
            runner.run([sys.executable, "-c", "import sys; sys.stderr.write('SYNTHETIC_CANARY'*20000); sys.exit(7)"])
        self.assertNotIn("SYNTHETIC_CANARY", str(caught.exception))
        log = self.destination / "command-01.stderr"
        self.assertEqual(log.stat().st_size, backup.LOG_LIMIT)
        self.assertEqual(stat.S_IMODE(log.stat().st_mode), 0o600)

    def test_command_timeout_fails_without_open_pipe_warnings(self):
        runner = backup.Runner(self.destination)
        with self.assertRaises(backup.BackupError):
            runner.run([sys.executable, "-c", "import time; time.sleep(10)"], timeout=0.05)

    def test_exit_zero_stderr_overflow_fails_even_for_streamed_output(self):
        runner = backup.Runner(self.destination)
        with backup.private_file(self.destination / "synthetic.dump") as output, self.assertRaises(backup.BackupError):
            runner.run([sys.executable, "-c", "import sys;sys.stderr.write('SYNTHETIC_WARNING'*20000);sys.stdout.buffer.write(b'PGDMPfixture')"], output=output)
        self.assertEqual((self.destination / "command-01.stderr").stat().st_size, backup.LOG_LIMIT)

    def test_docker_environment_cannot_redirect_local_target(self):
        runner = backup.Runner(self.destination)
        inherited = {"DOCKER_HOST": "tcp://unapproved.invalid:2376", "DOCKER_CONTEXT": "unapproved",
                     "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": "/unapproved", "DOCKER_API_VERSION": "1.10"}
        with patch.dict(os.environ, inherited):
            captured = runner.run([sys.executable, "-c", "import json,os;print(json.dumps([k for k in os.environ if k.startswith('DOCKER_')]))"])
        self.assertEqual(json.loads(captured), [])
        fixture = FixtureRunner()
        backup.check(self.args, fixture)
        for command in fixture.calls:
            self.assertEqual(command[:3], ["docker", "--host", "unix://" + self.socket_path])

    def test_non_socket_docker_targets_are_rejected(self):
        for value in ("tcp://unapproved.invalid:2376", "relative.sock", str(self.destination), str(self.destination / "missing")):
            with self.subTest(value=value), self.assertRaises(backup.BackupError):
                backup.validate_docker_socket(value)

    def test_non_read_only_session_is_rejected(self):
        class WritableSession(FixtureRunner):
            def run(self, command, **kwargs):
                result = super().run(command, **kwargs)
                if "psql" in command:
                    metadata = json.loads(result)
                    metadata["transaction_read_only"] = "off"
                    return json.dumps(metadata).encode()
                return result
        with self.assertRaises(backup.BackupError):
            backup.check(self.args, WritableSession())

    def test_explicit_export_has_hashes_toc_and_no_restore_claim(self):
        checked = backup.check(self.args, FixtureRunner())
        with patch.object(backup, "Runner", FixtureRunner):
            bundle = backup.execute(self.args, checked)
        self.assertEqual(stat.S_IMODE(bundle.stat().st_mode), 0o700)
        manifest = json.loads((bundle / "manifest.json").read_text())
        self.assertTrue(manifest["complete"])
        self.assertFalse(manifest["restore_verified"])
        self.assertEqual(manifest["source"]["postgres_major"], 16)
        for name, metadata in manifest["files"].items():
            file = bundle / name
            self.assertEqual(stat.S_IMODE(file.stat().st_mode), 0o600)
            self.assertEqual(hashlib.sha256(file.read_bytes()).hexdigest(), metadata["sha256"])
        self.assertEqual(len((bundle / "SHA256SUMS").read_text().splitlines()), 3)

    def test_failed_dump_cannot_gain_complete_manifest(self):
        class FailsDump(FixtureRunner):
            def run(self, command, **kwargs):
                if "pg_dump" in command and "--version" not in command:
                    raise backup.BackupError("synthetic dump failure")
                return super().run(command, **kwargs)
        checked = backup.check(self.args, FixtureRunner())
        with patch.object(backup, "Runner", FailsDump), self.assertRaises(backup.BackupError):
            backup.execute(self.args, checked)
        bundles = list(self.destination.iterdir())
        self.assertEqual(len(bundles), 1)
        self.assertFalse((bundles[0] / "manifest.json").exists())

    def test_changed_identity_before_export_prevents_dump(self):
        checked = backup.check(self.args, FixtureRunner())
        self.args.expected_image_id = "sha256:" + "c" * 64
        with patch.object(backup, "Runner", FixtureRunner), self.assertRaises(backup.BackupError):
            backup.execute(self.args, checked)
        bundle = next(self.destination.iterdir())
        self.assertFalse((bundle / "database.dump").exists())

    def test_invalid_archive_or_empty_toc_cannot_gain_manifest(self):
        for stage in ("pg_dump", "pg_restore"):
            class InvalidArchive(FixtureRunner):
                def run(self, command, **kwargs):
                    if stage in command and "--version" not in command:
                        kwargs["output"].write(b"not a custom archive" if stage == "pg_dump" else b"; no entries\n")
                        return b""
                    return super().run(command, **kwargs)
            checked = backup.check(self.args, FixtureRunner())
            with self.subTest(stage=stage), patch.object(backup, "Runner", InvalidArchive), self.assertRaises(backup.BackupError):
                backup.execute(self.args, checked)
        self.assertFalse(list(self.destination.glob("*/manifest.json")))

    def test_exit_zero_dump_warnings_cannot_gain_complete_manifest(self):
        real_runner = backup.Runner
        for stage in ("pg_dump", "pg_dumpall"):
            class WarningDump(FixtureRunner):
                def __init__(self, directory):
                    super().__init__(directory)
                    self.real = real_runner(directory)

                def run(self, command, **kwargs):
                    if stage in command and "--version" not in command:
                        script = "import sys;sys.stderr.write('SYNTHETIC_WARNING: omitted object');sys.stdout.buffer.write(b'PGDMPfixture')"
                        return self.real.run([sys.executable, "-c", script], **kwargs)
                    return super().run(command, **kwargs)
            captured = io.StringIO()
            with self.subTest(stage=stage), patch.object(backup, "Runner", WarningDump), contextlib.redirect_stderr(captured), self.assertRaises(backup.BackupError):
                backup.execute(self.args, {})
            self.assertNotIn("SYNTHETIC_WARNING", captured.getvalue())
        self.assertFalse(list(self.destination.glob("*/manifest.json")))

    def test_manifest_file_fsync_failure_leaves_no_complete_marker(self):
        calls = [0]

        def fail_pending_manifest(_):
            calls[0] += 1
            if calls[0] == 2:  # SHA256SUMS is first, pending manifest second.
                raise OSError("synthetic manifest fsync failure")

        with patch.object(backup, "Runner", FixtureRunner), patch.object(backup.os, "fsync", side_effect=fail_pending_manifest), self.assertRaises(backup.BackupError):
            backup.execute(self.args, {})
        self.assertGreaterEqual(calls[0], 2)
        self.assertFalse(list(self.destination.glob("*/manifest.json")))
        self.assertFalse(list(self.destination.glob("*/.manifest.pending")))

    def test_post_rename_directory_fsync_failure_removes_complete_marker(self):
        for failed_call in (2, 3):  # Bundle after rename, then its parent.
            calls = [0]
            saw_published_marker = [False]
            original = backup.fsync_directory

            def fail_directory(path):
                calls[0] += 1
                if calls[0] == failed_call:
                    saw_published_marker[0] = bool(list(self.destination.glob("*/manifest.json")))
                    raise OSError("synthetic directory fsync failure")
                return original(path)

            with self.subTest(failed_call=failed_call), patch.object(backup, "Runner", FixtureRunner), patch.object(backup, "fsync_directory", side_effect=fail_directory), self.assertRaises(backup.BackupError):
                backup.execute(self.args, {})
            self.assertTrue(saw_published_marker[0], "must exercise failure after atomic rename")
            self.assertFalse(list(self.destination.glob("*/manifest.json")))
            self.assertFalse(list(self.destination.glob("*/.manifest.pending")))

    def test_interruption_after_manifest_rename_removes_complete_marker(self):
        original = backup.os.replace
        renamed = [False]

        def interrupt_after_rename(source, target):
            original(source, target)
            renamed[0] = Path(target).exists()
            raise KeyboardInterrupt

        with patch.object(backup, "Runner", FixtureRunner), patch.object(backup.os, "replace", side_effect=interrupt_after_rename), self.assertRaises(KeyboardInterrupt):
            backup.execute(self.args, {})
        self.assertTrue(renamed[0])
        self.assertFalse(list(self.destination.glob("*/manifest.json")))
        self.assertFalse(list(self.destination.glob("*/.manifest.pending")))


@unittest.skipUnless(os.environ.get("RUN_SYNTHETIC_POSTGRES_BACKUP_TESTS") == "1", "local synthetic PostgreSQL integration is opt-in")
class SyntheticPostgresBackupTest(unittest.TestCase):
    def test_real_custom_dump_and_toc(self):
        name = "gnomonitoring-backup-synthetic-" + secrets.token_hex(4)
        local_socket = backup.validate_docker_socket(os.environ.get("SYNTHETIC_DOCKER_SOCKET", "/var/run/docker.sock"))
        docker = ["docker", "--host", "unix://" + local_socket]
        environment = backup.local_environment()
        container = subprocess.check_output([
            *docker, "run", "--rm", "--detach", "--network", "none", "--name", name,
            "--label", "com.docker.compose.project=synthetic-backup-fixture",
            "--tmpfs", "/var/lib/postgresql/data", "--env", "POSTGRES_HOST_AUTH_METHOD=trust",
            "--env", "POSTGRES_USER=backup_test", "--env", "POSTGRES_DB=backup_fixture", "postgres:16",
        ], text=True, env=environment).strip()
        self.addCleanup(lambda: subprocess.run([*docker, "rm", "--force", container], check=True, stdout=subprocess.DEVNULL, env=environment))
        deadline = time.monotonic() + 40
        while True:
            # The entrypoint's temporary initialization server has no TCP
            # listener. Waiting for this exact DB over loopback avoids racing
            # database creation or the temporary server's shutdown.
            ready = subprocess.run([*docker, "exec", container, "psql", "-X", "-w", "-h", "127.0.0.1",
                                    "-U", "backup_test", "-d", "backup_fixture", "-Atc", "SELECT 1"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=environment)
            if ready.returncode == 0:
                break
            if time.monotonic() >= deadline:
                self.fail("synthetic PostgreSQL did not become ready")
            time.sleep(0.25)
        subprocess.run([*docker, "exec", container, "psql", "-X", "-w", "-U", "backup_test", "-d", "backup_fixture",
            "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE backup_canary(id integer PRIMARY KEY, value text NOT NULL); INSERT INTO backup_canary VALUES (1, 'synthetic-only');"],
            check=True, stdout=subprocess.DEVNULL, env=environment)
        image = subprocess.check_output([*docker, "inspect", "--format", "{{.Image}}", container], text=True, env=environment).strip()
        with tempfile.TemporaryDirectory(prefix="synthetic-backup-output-") as tmp:
            destination = str(Path(tmp).resolve())
            output = io.StringIO()
            args = ["--container-id", container, "--expected-image-id", image,
                "--compose-project", "synthetic-backup-fixture", "--database", "backup_fixture", "--user", "backup_test",
                "--postgres-major", "16", "--destination", destination, "--dump-timeout-seconds", "30", "--execute"]
            args.extend(["--socket-directory", "/var/run/postgresql", "--port", "5432"])
            args.extend(["--docker-socket", local_socket])
            original_exec = backup.docker_exec

            def contaminated_exec(*args, **kwargs):
                command = original_exec(*args, **kwargs)
                # Per-exec variables only: never alter the server's own
                # startup environment. These must not select another server.
                inherited = ["--env", "PGHOSTADDR=203.0.113.10", "--env", "PGSERVICE=unapproved",
                             "--env", "PGHOST=unapproved.invalid", "--env", "PGPORT=6432"]
                index = command.index("exec") + 1
                return command[:index] + inherited + command[index:]

            with patch.object(backup, "docker_exec", side_effect=contaminated_exec), contextlib.redirect_stdout(output):
                self.assertEqual(backup.main(args), 0)
            bundle = next(Path(destination).iterdir())
            manifest = json.loads((bundle / "manifest.json").read_text())
            self.assertFalse(manifest["restore_verified"])
            self.assertIn("backup_canary", (bundle / "toc.list").read_text())
            self.assertTrue((bundle / "database.dump").read_bytes().startswith(b"PGDMP"))
            for file in bundle.iterdir():
                self.assertEqual(stat.S_IMODE(file.stat().st_mode), 0o600)


if __name__ == "__main__":
    unittest.main()
