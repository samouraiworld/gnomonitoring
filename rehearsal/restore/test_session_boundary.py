"""Controlled clone lifecycle fault injection without Docker or real data."""

from contextlib import ExitStack, redirect_stdout
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import stat
import unittest
from unittest import mock

import test_runner_boundary as BOUNDARY

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("session_boundary_runner", HERE / "runner.py")
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


class SessionBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.fixture = BOUNDARY.RestoreBoundaryTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.output = self.fixture.output
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(mock.patch.object(RUNNER.FENCES, "approved_socket", return_value=Path("/approved/local.sock")))
        self.runner = RUNNER.RestoreRunner(self.fixture.bundle, self.output, timeout_seconds=90)
        self.stack.enter_context(mock.patch.object(RUNNER.FENCES, "validate_config"))
        self.stack.enter_context(mock.patch.object(self.runner, "fresh_volumes"))
        self.stack.enter_context(mock.patch.object(self.runner, "inspect_volume_fence"))
        self.stack.enter_context(mock.patch.object(self.runner, "inspect_container"))
        self.stack.enter_context(mock.patch.object(self.runner, "wait_postgres"))
        self.stack.enter_context(mock.patch.object(self.runner, "disk_free", return_value=100 * RUNNER.GIB))
        self.cleanup = self.stack.enter_context(mock.patch.object(self.runner, "cleanup"))
        self.baseline = {"tables": [{"schema": "public", "name": "fixture", "rows": 1}]}
        self.stack.enter_context(mock.patch.object(self.runner, "baseline", return_value=self.baseline))
        self.seal = self.stack.enter_context(mock.patch.object(self.runner, "seal_marker", return_value={"dsn_mode": "0600"}))
        self.stack.enter_context(mock.patch.object(self.runner, "pin_sources", return_value={"fixture.go": "a" * 64}))
        self.initdb = self.stack.enter_context(mock.patch.object(self.runner, "start_initdb", side_effect=[({"pass": 1}, 3), ({"pass": 2}, 0)]))
        def command(arguments, **unused):
            if arguments[-2:] == ["--format", "{{.OSType}}"]:
                return "linux", 0
            if arguments[-3:] == ["config", "--format", "json"]:
                return '{"services":{},"volumes":{}}', 0
            if arguments[-4:] == ["ps", "--all", "--quiet", "postgres"]:
                return "b" * 64, 0
            if arguments[-4:] == ["ps", "--all", "--quiet", "initdb"]:
                return "c" * 64, 0
            return "", 0
        self.stack.enter_context(mock.patch.object(self.runner, "command", side_effect=command))
        def pg(arguments, **unused):
            if arguments == ["pg_restore", "--list"]:
                return (self.fixture.bundle / "toc.list").read_text()
            return ""
        self.pg = self.stack.enter_context(mock.patch.object(self.runner, "exec_pg", side_effect=pg))
        self.sql = self.stack.enter_context(mock.patch.object(self.runner, "sql", return_value="160015"))

    def summary(self):
        return self.output / "restore-summary.json"

    def journal(self):
        return [json.loads(line) for line in (self.output / "session-journal.private.jsonl").read_text().splitlines()]

    def test_live_target_closes_before_publication_and_copies_cannot_forge_report(self):
        previous = signal.getsignal(signal.SIGTERM)
        with self.runner.open_clone() as session:
            self.cleanup.assert_not_called()
            self.assertFalse(self.summary().exists())
            session.baseline["tables"].clear()
            session.report["restore_verified"] = False
            self.assertEqual(len(session.baseline["tables"]), 1)
            self.assertNotIn("owned_resources_cleanup_verified", session.report)
            self.assertEqual(session.sql("SELECT 1", timeout=90), "160015")
            self.sql.assert_called_with("/* controlled clone session */\nSELECT 1", timeout=90)
        self.assertIs(signal.getsignal(signal.SIGTERM), previous)
        self.cleanup.assert_called_once_with("b" * 64)
        report = json.loads(self.summary().read_text())
        self.assertTrue(report["restore_verified"])
        self.assertTrue(report["owned_resources_cleanup_verified"])
        with self.assertRaisesRegex(RUNNER.Refused, "clone_session_is_closed"):
            session.sql("SELECT 1")
        records = self.journal()
        self.assertIn("cleanup_verified", [record["event"] for record in records])
        self.assertEqual(records[0]["clone_id"], self.runner.clone_id)
        self.assertEqual(stat.S_IMODE((self.output / "session-journal.private.jsonl").stat().st_mode), 0o600)
        self.assertNotIn(self.runner.environment["RESTORE_ADMIN_PASSWORD"], json.dumps(records))

    def test_body_exception_cleanup_without_summary(self):
        # One instance per subtest is covered separately by test_new_attempts.
        with self.assertRaises(RuntimeError):
            with self.runner.open_clone():
                raise RuntimeError("private callback failure")
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())
        self.assertIn("session_failed", [record["event"] for record in self.journal()])

    def test_keyboard_interrupt_cleanup_without_summary(self):
        with self.assertRaises(KeyboardInterrupt):
            with self.runner.open_clone():
                raise KeyboardInterrupt
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())

    def test_sigterm_cleanup_and_previous_handler_restored(self):
        previous = signal.getsignal(signal.SIGTERM)
        with self.assertRaises(KeyboardInterrupt):
            with self.runner.open_clone():
                os.kill(os.getpid(), signal.SIGTERM)
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())
        self.assertIs(signal.getsignal(signal.SIGTERM), previous)

    def test_caught_sigterm_poisoned_session_cannot_publish(self):
        with self.assertRaisesRegex(RUNNER.Refused, "clone_session_operation_failed"):
            with self.runner.open_clone():
                try:
                    os.kill(os.getpid(), signal.SIGTERM)
                except KeyboardInterrupt:
                    pass
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())

    def test_first_sigterm_during_cleanup_finishes_removal_but_cannot_publish(self):
        completed = []
        def cleanup(_container):
            os.kill(os.getpid(), signal.SIGTERM)
            completed.append(True)
        self.cleanup.side_effect = cleanup
        previous = signal.getsignal(signal.SIGTERM)
        with self.assertRaisesRegex(RUNNER.Refused, "clone_session_operation_failed"):
            with self.runner.open_clone():
                pass
        self.assertEqual(completed, [True])
        self.cleanup.assert_called_once()
        self.assertIn("cleanup_verified", [record["event"] for record in self.journal()])
        self.assertFalse(self.summary().exists())
        self.assertIs(signal.getsignal(signal.SIGTERM), previous)

    def test_swallowed_sql_failure_poisoned_session_cannot_publish(self):
        with self.assertRaisesRegex(RUNNER.Refused, "clone_session_operation_failed"):
            with self.runner.open_clone() as session:
                self.sql.side_effect = RUNNER.Refused("private query detail")
                with self.assertRaises(RUNNER.Refused):
                    session.sql("SELECT broken")
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())

    def test_sql_budgets_and_target_cannot_be_overridden(self):
        with self.assertRaisesRegex(RUNNER.Refused, "session_sql_budget_invalid"):
            with self.runner.open_clone() as session:
                session.sql("SELECT 1", timeout=91)
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())

    def test_initdb_is_separate_after_baseline_and_preserves_review(self):
        with self.runner.open_clone() as session:
            self.initdb.assert_not_called()
            self.assertIsNone(session.report["initdb_provenance"])
            before = session.baseline
            result = session.run_initdb()
            self.assertTrue(result["review_required"])
            self.assertEqual(before, session.baseline)
            self.assertEqual(session.report["initdb_provenance"], {"fixture.go": "a" * 64})
        self.assertTrue(json.loads(self.summary().read_text())["initdb"]["review_required"])

    def test_execute_keeps_optional_initdb_contract(self):
        report = self.runner.execute(run_initdb=True)
        self.assertTrue(report["initdb"]["review_required"])
        self.assertTrue(report["owned_resources_cleanup_verified"])
        self.cleanup.assert_called_once()

    def test_phase_failures_never_yield_or_publish_and_cleanup_preserves_original(self):
        # Restoration, ANALYZE and marker failures use the actual preparation path.
        self.pg.side_effect = RUNNER.Refused("initial_restore_failure")
        self.cleanup.side_effect = RUNNER.Refused("secondary_cleanup_failure")
        with redirect_stdout(io.StringIO()), self.assertRaisesRegex(RUNNER.Refused, "^initial_restore_failure$"):
            with self.runner.open_clone():
                self.fail("failed restoration yielded a session")
        self.cleanup.assert_called_once()
        self.seal.assert_not_called()
        self.assertFalse(self.summary().exists())
        self.assertIn("cleanup_unverified", [record["event"] for record in self.journal()])
        self.assertNotIn("cleanup_verified", [record["event"] for record in self.journal()])

    def test_analyze_failure_cannot_seal_or_yield(self):
        def sql(query, **unused):
            if query.startswith("ANALYZE"):
                raise RUNNER.Refused("analyze_failure")
            return "160015"
        self.sql.side_effect = sql
        with self.assertRaisesRegex(RUNNER.Refused, "analyze_failure"):
            with self.runner.open_clone():
                self.fail("analysis failed")
        self.seal.assert_not_called()
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())

    def test_seal_failure_cannot_yield(self):
        self.seal.side_effect = RUNNER.Refused("seal_failure")
        with self.assertRaisesRegex(RUNNER.Refused, "seal_failure"):
            with self.runner.open_clone():
                self.fail("seal failed")
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())

    def test_initdb_failure_cannot_publish_even_if_caught_by_body(self):
        self.initdb.side_effect = RUNNER.Refused("initdb_failure")
        with self.assertRaisesRegex(RUNNER.Refused, "clone_session_operation_failed"):
            with self.runner.open_clone() as session:
                with self.assertRaisesRegex(RUNNER.Refused, "initdb_failure"):
                    session.run_initdb()
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())

    def test_cleanup_failure_blocks_completion(self):
        self.cleanup.side_effect = RUNNER.Refused("cleanup_failure")
        with self.assertRaisesRegex(RUNNER.Refused, "cleanup_failure"):
            with self.runner.open_clone():
                pass
        self.assertFalse(self.summary().exists())
        self.assertNotIn("cleanup_verified", [record["event"] for record in self.journal()])

    def test_journal_failure_cannot_prevent_cleanup(self):
        original = self.runner._journal
        def journal(event, **details):
            if self.runner.stage == "cleanup_owned_resources":
                raise OSError("injected journal disk error")
            original(event, **details)
        with mock.patch.object(self.runner, "_journal", side_effect=journal), self.assertRaises(OSError):
            with self.runner.open_clone():
                pass
        self.cleanup.assert_called_once()
        self.assertFalse(self.summary().exists())

    def test_publication_failure_happens_after_cleanup_and_leaves_no_summary(self):
        with mock.patch.object(RUNNER, "publish_summary", side_effect=OSError("publication_failure")):
            with self.assertRaises(OSError):
                with self.runner.open_clone():
                    pass
        self.cleanup.assert_called_once()
        self.assertIn("cleanup_verified", [record["event"] for record in self.journal()])
        self.assertFalse(self.summary().exists())

    def test_new_attempts_use_new_ids_and_cannot_resume_previous_runner(self):
        self.runner.execute()
        with self.assertRaisesRegex(RUNNER.Refused, "clone_session_is_single_use"):
            with self.runner.open_clone():
                self.fail("reused runner")
        next_output = self.fixture.root / "next-output"
        next_output.mkdir(mode=0o700)
        next_runner = RUNNER.RestoreRunner(self.fixture.bundle, next_output)
        self.assertNotEqual(self.runner.clone_id, next_runner.clone_id)
        self.assertNotEqual(self.runner.project, next_runner.project)
        self.assertNotEqual(self.runner.database, next_runner.database)


if __name__ == "__main__":
    unittest.main()
