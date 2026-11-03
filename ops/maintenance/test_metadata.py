import json
from pathlib import Path
import unittest

import metadata


class MaintenanceMetadataTest(unittest.TestCase):
    def test_only_fixed_read_commands_and_no_provider_writes(self):
        calls = []
        def runner(command):
            calls.append(command)
            if command == metadata.COMMANDS["packages"]:
                return 1, "openssl\t3.0.0\nsecret-package\tdo-not-emit\n"
            if command == metadata.COMMANDS["lifecycle"]:
                return 0, json.dumps({"Rules": []})
            return 0, "1.2.3\n"
        result = metadata.collect(runner)
        self.assertEqual(set(calls), set(metadata.COMMANDS.values()))
        self.assertEqual(result["installed_packages"], {"openssl": "3.0.0"})
        self.assertNotIn("do-not-emit", json.dumps(result))
        self.assertTrue(result["read_only"])

    def test_arbitrary_command_is_rejected_before_process(self):
        with self.assertRaises(ValueError):
            metadata.run_command(("aws", "s3", "rm", "s3://gnomonitoring/"))

    def test_global_rule_covers_postgres_without_exposing_rule_id(self):
        result = metadata.lifecycle_summary({"Rules": [{"ID": "do-not-emit", "Status": "Enabled",
            "Filter": {"Prefix": ""}, "Expiration": {"Days": 7}}]})
        self.assertEqual(result["postgres_rules"][0], {"enabled": True, "scope": "entire_prefix",
            "additional_conditions": False, "expiration_days": 7,
            "fixed_expiration_date_present": False, "noncurrent_expiration_present": False})
        self.assertNotIn("do-not-emit", json.dumps(result))

    def test_unrelated_prefix_is_counted_without_disclosure(self):
        result = metadata.lifecycle_summary({"Rules": [{"Filter": {"Prefix": "private-unrelated/"}}]})
        self.assertEqual(result["unrelated_rule_count"], 1)
        self.assertNotIn("private-unrelated", json.dumps(result))

    def test_partial_and_tagged_rules_are_not_unconditional_full_coverage(self):
        result = metadata.lifecycle_summary({"Rules": [
            {"Status": "Enabled", "Filter": {"Prefix": "postgres/partial/"}},
            {"Status": "Enabled", "Filter": {"And": {"Prefix": "postgres/", "Tags": [{"Key": "x", "Value": "hidden"}]}}, "Expiration": {"Days": 30}}]})
        self.assertEqual(result["postgres_rules"][0]["scope"], "partial_prefix")
        self.assertTrue(result["postgres_rules"][1]["additional_conditions"])
        self.assertNotIn("hidden", json.dumps(result))

    def test_remote_source_is_exactly_the_tested_source(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / ".github/workflows/main.yml").read_text()
        embedded = workflow.split("<<'REMOTE_BACKUP_INVENTORY'\n", 1)[1].split('          REMOTE_BACKUP_INVENTORY', 1)[0]
        actual = ''.join(line[10:] + '\n' for line in embedded.splitlines())
        self.assertEqual(actual, (root / "ops/maintenance/metadata.py").read_text())


if __name__ == "__main__":
    unittest.main()
