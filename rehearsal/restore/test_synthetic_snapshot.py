"""Independent unit checks for synthetic source equality and receipt integrity."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("tested_synthetic_snapshot", HERE / "synthetic_snapshot.py")
ORACLE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ORACLE)


class SnapshotTests(unittest.TestCase):
    def test_row_order_independent_but_duplicates_preserved(self):
        rows = [["chain-a", "1"], ["chain-b", "2"], ["chain-a", "1"]]
        first = ORACLE.content_fingerprints(rows, ["chain_id", "payload"])
        self.assertEqual(first, ORACLE.content_fingerprints(list(reversed(rows)), ["chain_id", "payload"]))
        self.assertNotEqual(first["payload_sha256"], ORACLE.content_fingerprints(rows[:2], ["chain_id", "payload"])["payload_sha256"])

    def test_equal_count_changed_payload_is_rejected(self):
        source = ORACLE.content_fingerprints([["a", "10"], ["b", "20"]], ["chain_id", "payload"])
        target = ORACLE.content_fingerprints([["a", "11"], ["b", "20"]], ["chain_id", "payload"])
        self.assertEqual(source["rows"], target["rows"])
        with self.assertRaises(ORACLE.SnapshotMismatch) as error:
            ORACLE.compare_snapshots(source, target)
        self.assertIn("snapshot.payload_sha256", error.exception.differences)
        self.assertNotIn("11", str(error.exception))

    def test_null_empty_and_global_chain_buckets_are_distinct(self):
        actual = ORACLE.content_fingerprints([[None, "x"], ["", "x"], ["chain-a", "x"]], ["chain_id", "payload"])
        self.assertEqual({(item["scope"], item["chain_id"]) for item in actual["scopes"]},
                         {("null_chain_id", None), ("chain", ""), ("chain", "chain-a")})
        global_rows = ORACLE.content_fingerprints([[None], [""]], ["payload"])
        self.assertEqual(global_rows["scopes"][0]["scope"], "global")
        self.assertNotEqual(ORACLE.content_fingerprints([[None]], ["payload"])["payload_sha256"],
                            ORACLE.content_fingerprints([[""]], ["payload"])["payload_sha256"])

    def test_metadata_loss_and_unexpected_metadata_both_fail(self):
        source = {"relations": [{"constraints": [{"name": "positive", "validated": True}], "indexes": [{"definition": "CREATE INDEX"}]}]}
        for field in ("constraints", "indexes"):
            target = copy.deepcopy(source)
            target["relations"][0].pop(field)
            with self.assertRaises(ORACLE.SnapshotMismatch):
                ORACLE.compare_snapshots(source, target)
        target = copy.deepcopy(source)
        target["extra"] = True
        with self.assertRaises(ORACLE.SnapshotMismatch):
            ORACLE.compare_snapshots(source, target)

    def test_owner_mapping_only_changes_explicit_metadata_fields(self):
        source = {"owner": "fixture_owner", "acl": [{"grantor": "fixture_owner", "grantee": "fixture_owner"}],
                  "content": {"owner": ["fixture_owner"]}, "name": "fixture_owner", "arbitrary": "fixture_admin"}
        normalized = ORACLE._normalize_ownership(source)
        self.assertEqual(normalized["owner"], "rehearsal_owner")
        self.assertEqual(normalized["acl"][0], {"grantor": "rehearsal_owner", "grantee": "rehearsal_owner"})
        self.assertEqual(normalized["name"], "fixture_owner")
        self.assertEqual(normalized["arbitrary"], "fixture_admin")
        self.assertEqual(normalized["content"], source["content"])
        self.assertEqual(ORACLE._normalize_ownership({"owner": "other_owner"}), {"owner": "other_owner"})

    def test_non_text_row_encoding_refused(self):
        with self.assertRaises(ORACLE.SnapshotError):
            ORACLE.content_fingerprints([[1]], ["integer_column"])
        with self.assertRaises(ORACLE.SnapshotError):
            ORACLE.content_fingerprints([["a", "extra"]], ["payload"])


class ReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="synthetic-receipt-unit-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.bundle = self.root / "bundles" / "invented-unit-bundle"
        self.bundle.mkdir(parents=True)
        for name in ("database.dump", "globals.sql", "toc.list", "manifest.json", "SHA256SUMS"):
            (self.bundle / name).write_bytes(("invented-unit-" + name).encode())
        self.path = ORACLE.write_source_receipt(self.bundle, {"relations": []}, "16.15 (unit test)", ["archive.sql"])

    def reseal(self, receipt):
        receipt.pop("receipt_sha256", None)
        receipt["receipt_sha256"] = ORACLE.sha256(ORACLE.canonical_bytes(receipt))
        self.path.write_bytes(ORACLE.canonical_bytes(receipt))

    def test_receipt_round_trip_binds_actual_generator_fixture_oracle_and_archive(self):
        receipt = ORACLE.read_source_receipt(self.bundle)
        self.assertEqual(receipt["archive_sha256"], ORACLE.sha256((self.bundle / "database.dump").read_bytes()))
        self.assertEqual(receipt["provenance"], ORACLE.source_provenance(["archive.sql"]))
        self.assertEqual(set(receipt["provenance"]), {"rehearsal/restore/synthetic_bundle.py", "rehearsal/restore/synthetic_snapshot.py", "scripts/backup/prepare-postgres-backup.py", "rehearsal/restore/fixtures/archive.sql"})
        self.assertFalse(receipt["source_snapshot_equality_verified"])
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)

    def test_archive_modified_after_receipt_is_rejected(self):
        (self.bundle / "database.dump").write_bytes(b"invented changed archive")
        with self.assertRaisesRegex(ORACLE.SnapshotError, "archive_mismatch"):
            ORACLE.read_source_receipt(self.bundle)

    def test_snapshot_mutation_without_receipt_reseal_fails(self):
        receipt = json.loads(self.path.read_bytes())
        receipt["snapshot"]["relations"].append({"unexpected": True})
        self.path.write_text(json.dumps(receipt))
        with self.assertRaisesRegex(ORACLE.SnapshotError, "receipt_digest_mismatch"):
            ORACLE.read_source_receipt(self.bundle)

    def test_snapshot_mutation_with_outer_reseal_still_fails_inner_digest(self):
        receipt = json.loads(self.path.read_bytes())
        receipt["snapshot"]["relations"].append({"unexpected": True})
        self.reseal(receipt)
        with self.assertRaisesRegex(ORACLE.SnapshotError, "snapshot_digest_mismatch"):
            ORACLE.read_source_receipt(self.bundle)

    def test_resealed_wrong_provenance_or_image_is_rejected(self):
        original = json.loads(self.path.read_bytes())
        for field in ("provenance", "source_postgres_image", "source_postgres_version"):
            receipt = copy.deepcopy(original)
            receipt[field] = {} if field == "provenance" else "unexpected"
            self.reseal(receipt)
            with self.assertRaises(ORACLE.SnapshotError):
                ORACLE.read_source_receipt(self.bundle)

    def test_symlink_and_public_receipt_are_rejected(self):
        self.path.chmod(0o644)
        with self.assertRaisesRegex(ORACLE.SnapshotError, "receipt_file"):
            ORACLE.read_source_receipt(self.bundle)
        self.path.chmod(0o600)
        alias = self.root / "alias"
        alias.symlink_to(self.path)
        with self.assertRaisesRegex(ORACLE.SnapshotError, "receipt_file"):
            ORACLE.read_source_receipt(self.bundle, alias)


if __name__ == "__main__":
    unittest.main()
