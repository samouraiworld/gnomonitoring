"""Synthetic-only live session lifecycle; Docker execution requires an opt-in."""

import contextlib
import importlib.util
import io
import json
import os
import signal
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


RUNNER = load_module("controlled_restore_runner", HERE / "runner.py")
FIXTURE = load_module("controlled_restore_fixture", HERE / "synthetic_bundle.py")


def private_directory(parent, name):
    path = parent / name
    path.mkdir(mode=0o700)
    return path


class SessionBoundaries(unittest.TestCase):
    def test_default_cli_remains_tools_only(self):
        with mock.patch.object(RUNNER, "RestoreRunner") as constructor, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(RUNNER.main([]), 0)
        constructor.assert_not_called()
        self.assertEqual(json.loads(output.getvalue()), {"status": "tools_only", "action": "none"})


class SyntheticSessionBase(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        provided = os.environ.get(getattr(cls, "private_output_environment", "CONTROLLED_SESSION_PRIVATE_OUTPUT"))
        if provided:
            cls.root = FIXTURE.private_empty_directory(provided)
        else:
            temporary = tempfile.TemporaryDirectory(prefix="gnomon-session-e2e-")
            cls.addClassCleanup(temporary.cleanup)
            cls.root = Path(temporary.name).resolve()
            cls.root.chmod(0o700)
        options = {"capture_source": True} if getattr(cls, "capture_source", False) else {}
        cls.bundle = FIXTURE.create_bundle(private_directory(cls.root, "invented-source"), **options)

    def runner(self, name):
        output = private_directory(self.root, name)
        return RUNNER.RestoreRunner(self.bundle, output, timeout_seconds=120), output

    def assert_cleaned(self, runner, output, *, completed):
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith(("DOCKER_", "BUILDX_")) and key != "BUILDKIT_HOST"}
        selectors = [
            ["ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project=" + runner.project],
            ["volume", "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + runner.project],
            ["image", "ls", "--quiet", "--filter", "reference=" + runner.project + "-*:local-only"],
        ]
        for selector in selectors:
            result = subprocess.run(runner.docker + selector, env={**environment, "DOCKER_CONFIG": str(output / "docker-config")},
                                    capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, "owned-resource inspection failed; details remain private")
            self.assertFalse(result.stdout.strip(), "owned resources remain after session exit")
        summary = output / "restore-summary.json"
        self.assertEqual(summary.exists(), completed, "completed summary state differed")
        journal = output / "session-journal.private.jsonl"
        self.assertTrue(journal.is_file(), "private progress journal missing")
        self.assertEqual(stat.S_IMODE(journal.stat().st_mode), 0o600)
        events = [json.loads(line) for line in journal.read_text().splitlines()]
        self.assertTrue(any(event.get("event") == "cleanup_verified" for event in events),
                        "journal did not confirm cleanup")
        if completed:
            self.assertTrue(json.loads(summary.read_text())["owned_resources_cleanup_verified"])


@unittest.skipUnless(os.environ.get("RUN_CONTROLLED_CLONE_SESSION_TESTS") == "1", "synthetic controlled session is opt-in")
class ControlledSessionTests(SyntheticSessionBase):
    def test_live_session_cleanup_and_fresh_retry(self):
        identities = []
        for name in ("first-session", "fresh-session"):
            runner, output = self.runner(name)
            identities.append((runner.clone_id, runner.project, runner.database))
            with runner.open_clone() as session:
                self.assertEqual(session.sql("SELECT count(*) FROM public.archive_probe").strip(), "5")
                self.assertFalse((output / "restore-summary.json").exists(), "live session gained a final summary")
                session.sql("DELETE FROM public.archive_probe WHERE id=1")
                self.assertEqual(session.sql("SELECT count(*) FROM public.archive_probe").strip(), "4")
            self.assert_cleaned(runner, output, completed=True)
            with self.assertRaises(RUNNER.Refused):
                session.sql("SELECT 1")
        for position in range(3):
            self.assertNotEqual(identities[0][position], identities[1][position], "session identity was reused")

    def test_callback_failures_and_interruptions_cleanup_without_final_summary(self):
        for name, failure in (("callback-failure", RuntimeError), ("callback-interrupt", KeyboardInterrupt)):
            with self.subTest(case=name):
                runner, output = self.runner(name)
                with self.assertRaises(failure):
                    with runner.open_clone() as session:
                        self.assertEqual(session.sql("SELECT 1").strip(), "1")
                        raise failure("invented callback interruption")
                self.assert_cleaned(runner, output, completed=False)
        runner, output = self.runner("callback-caught-sigterm")
        with self.assertRaisesRegex(RUNNER.Refused, "^clone_session_operation_failed$"):
            with runner.open_clone() as session:
                self.assertEqual(session.sql("SELECT 1").strip(), "1")
                try:
                    os.kill(os.getpid(), signal.SIGTERM)
                except KeyboardInterrupt:
                    pass  # A caller swallowing the signal must not restore success.
        self.assert_cleaned(runner, output, completed=False)


if __name__ == "__main__":
    unittest.main()
