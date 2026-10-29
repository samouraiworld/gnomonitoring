"""Invented frozen-source oracle; no operator archive or endpoint is accepted."""

import copy
import os
from pathlib import Path
import unittest

from test_controlled_session import SyntheticSessionBase, load_module

HERE = Path(__file__).resolve().parent
ORACLE = load_module("synthetic_restore_snapshot", HERE / "synthetic_snapshot.py")


class OracleBoundaries(unittest.TestCase):
    def test_payload_difference_is_not_hidden_by_equal_counts(self):
        source = {"tables": [{"schema": "public", "name": "invented", "rows": 2,
                              "payload_sha256": "a" * 64}]}
        target = copy.deepcopy(source)
        target["tables"][0]["payload_sha256"] = "b" * 64
        with self.assertRaises(ORACLE.SnapshotMismatch):
            ORACLE.compare_snapshots(source, target)

    def test_equal_snapshots_compare_without_mutation(self):
        source = {"tables": [{"schema": "public", "name": "invented", "rows": 2,
                              "payload_sha256": "a" * 64}]}
        before = copy.deepcopy(source)
        ORACLE.compare_snapshots(source, copy.deepcopy(source))
        self.assertEqual(source, before)


@unittest.skipUnless(os.environ.get("RUN_SYNTHETIC_RESTORE_ORACLE_TESTS") == "1", "synthetic source oracle is opt-in")
class SyntheticSourceOracleTests(SyntheticSessionBase):
    capture_source = True
    private_output_environment = "SYNTHETIC_ORACLE_PRIVATE_OUTPUT"

    def test_source_equality_and_same_count_payload_mutation(self):
        receipt = ORACLE.read_source_receipt(self.bundle)
        self.assertIn("snapshot", receipt)
        runner, output = self.runner("source-equality")
        with runner.open_clone(run_initdb=False) as session:
            target = ORACLE.capture_snapshot(session.sql)
            ORACLE.compare_snapshots(receipt["snapshot"], target)
            phase = session.run_initdb()
            self.assertTrue(phase["review_required"], "invented InitDB reduction was not retained for review")
        self.assert_cleaned(runner, output, completed=True)
        # This is a fresh restore, not the post-migration database above.
        runner, output = self.runner("same-count-corruption")
        with self.assertRaises(ORACLE.SnapshotMismatch):
            with runner.open_clone() as session:
                rows_before = session.sql("SELECT count(*) FROM public.archive_probe").strip()
                session.sql("UPDATE public.archive_probe SET marker='changed' WHERE id=1")
                self.assertEqual(session.sql("SELECT count(*) FROM public.archive_probe").strip(), rows_before)
                target = ORACLE.capture_snapshot(session.sql)
                ORACLE.compare_snapshots(receipt["snapshot"], target)
        self.assert_cleaned(runner, output, completed=False)


if __name__ == "__main__":
    unittest.main()
