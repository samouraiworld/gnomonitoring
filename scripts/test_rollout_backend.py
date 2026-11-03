"""Test mutation boundaries with a Docker command oracle, never production."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("rollout_backend.py")
BACKEND = "a" * 64
DATABASE = "b" * 64
OLD = "sha256:" + "c" * 64
NEW = "sha256:" + "d" * 64
DIGEST = "ghcr.io/samouraiworld/gnomonitoring-server@sha256:" + "e" * 64


class RolloutTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.project = self.root / "project with spaces"
        self.project.mkdir()
        self.base = self.project / "production.yml"
        self.base.write_text("services: {}\n")
        self.extra = self.project / "operator override.yml"
        self.extra.write_text("services: {}\n")
        self.log = self.root / "calls.jsonl"
        self.receipt = self.root / "receipt.json"
        self.fake = self.root / "docker"
        self.fake.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
a = sys.argv[1:]
root = pathlib.Path(os.environ['FIXTURE_ROOT'])
project = root / 'project with spaces'
with (root / 'calls.jsonl').open('a') as stream: stream.write(json.dumps(a) + '\\n')
applied = (root / 'applied').exists()
if a[0] == 'compose':
    if 'up' in a:
        (root / 'applied').touch()
        if os.environ.get('UP_FAIL'): print('secret-runtime-detail', file=sys.stderr); sys.exit(1)
    else:
        print(('f' if applied else 'a') * 64)
        if not os.environ.get('NO_DATABASE'): print('b' * 64)
elif a[0] == 'inspect':
    backend = a[-1] != 'b' * 64
    files = [str(project / 'production.yml'), str(project / 'operator override.yml')]
    if backend and applied: files.append(str(project / 'release.yml'))
    if os.environ.get('WRONG_FILES'): files.reverse()
    print(json.dumps(dict(id=a[-1], image='sha256:' + ('d' if backend and applied else 'c') * 64,
        status='exited' if backend and applied and os.environ.get('CRASH') else 'running',
        started='changed' if not backend and applied and os.environ.get('DATABASE_RESTART') else 'original',
        health=None if backend else ('unhealthy' if os.environ.get('DATABASE_UNHEALTHY') else 'healthy'), project='gnomonitoringv2',
        service='detect-proposal' if backend else 'postgres', directory=str(project), files=','.join(files))))
elif a[:2] == ['image', 'inspect']:
    new = a[-1].startswith('ghcr.io/')
    print(json.dumps(dict(id='sha256:' + ('d' if new else 'c') * 64,
        digests=[] if os.environ.get('NO_DIGEST') else ['ghcr.io/samouraiworld/gnomonitoring-server@sha256:' + ('e' if new else '9') * 64],
        revision=('0' if os.environ.get('WRONG_REVISION') else '1') * 40,
        source='unknown' if os.environ.get('WRONG_SOURCE') else 'https://github.com/samouraiworld/gnomonitoring')))
else:
    sys.exit(4)
''')
        self.fake.chmod(0o755)
        self.env = {**os.environ, "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
                    "FIXTURE_ROOT": str(self.root), "POSTGRES_PASSWORD": "secret-runtime-detail"}

    def run_script(self, *extra, **environment):
        command = [sys.executable, "-B", str(SCRIPT), "--project-directory", str(self.project),
                   "--project", "gnomonitoringv2", "--compose-file", str(self.base),
                   "--compose-file", str(self.extra), "--backend-container", BACKEND,
                   "--expected-backend-image", OLD, "--image", DIGEST,
                   "--revision", "1" * 40, "--override", "release.yml", *extra]
        return subprocess.run(command, capture_output=True, text=True,
                              env={**self.env, **environment})

    def calls(self):
        return [json.loads(row) for row in self.log.read_text().splitlines()]

    def assert_no_mutation(self):
        self.assertFalse((self.project / "release.yml").exists())
        self.assertFalse((self.root / "applied").exists())
        for call in self.calls():
            self.assertNotIn("up", call)
            self.assertNotIn("pull", call)
            self.assertNotIn("config", call)

    def test_default_plan_is_read_only_and_preserves_compose_order(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["status"], "plan_only")
        self.assert_no_mutation()
        self.assertFalse((self.project / ".backend-rollout.lock").exists())
        self.assertEqual(self.calls()[0], ["compose", "--project-name", "gnomonitoringv2",
            "--project-directory", str(self.project), "-f", str(self.base),
            "-f", str(self.extra), "ps", "--all", "--quiet"])

    def test_apply_only_one_backend_with_no_dependencies_build_or_pull(self):
        result = self.run_script("--apply", "--receipt", str(self.receipt))
        self.assertEqual(result.returncode, 0, result.stderr)
        writes = [call for call in self.calls() if "up" in call]
        self.assertEqual(len(writes), 1)
        # Assert the whole command rather than only token membership.
        self.assertEqual(writes[0], ["compose", "--project-name", "gnomonitoringv2",
            "--project-directory", str(self.project), "-f", str(self.base),
            "-f", str(self.extra), "-f", str(self.project / "release.yml"),
            "up", "-d", "--no-deps", "--no-build", "--pull", "never", "detect-proposal"])
        self.assertEqual((self.project / "release.yml").stat().st_mode & 0o777, 0o600)
        record = json.loads(result.stdout)
        self.assertTrue(record["applied"])
        self.assertEqual(record["functional_acceptance"], "pending")
        self.assertEqual(record["before"][DATABASE], record["after"][DATABASE])
        self.assertEqual(json.loads(self.receipt.read_text())["status"], "preflight_record_not_completion")
        self.assertTrue(Path(str(self.receipt) + ".applied.json").is_file())

    def test_tag_instead_of_digest_is_refused_before_docker(self):
        result = self.run_script("--image", "ghcr.io/samouraiworld/gnomonitoring-server:latest")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())

    def test_short_revision_is_refused_before_docker(self):
        self.assertNotEqual(self.run_script("--revision", "ab5d877").returncode, 0)
        self.assertFalse(self.log.exists())

    def test_compose_order_mismatch_is_refused_without_mutation(self):
        result = self.run_script("--apply", "--receipt", str(self.receipt), WRONG_FILES="1")
        self.assertNotEqual(result.returncode, 0)
        self.assert_no_mutation()

    def test_wrong_candidate_revision_is_refused_without_mutation(self):
        result = self.run_script("--apply", "--receipt", str(self.receipt), WRONG_REVISION="1")
        self.assertNotEqual(result.returncode, 0)
        self.assert_no_mutation()

    def test_wrong_source_or_missing_digest_is_refused_without_mutation(self):
        for variable in ["WRONG_SOURCE", "NO_DIGEST"]:
            with self.subTest(variable=variable):
                result = self.run_script("--apply", "--receipt", str(self.receipt), **{variable: "1"})
                self.assertNotEqual(result.returncode, 0)
                self.assert_no_mutation()

    def test_absent_or_unhealthy_database_prevents_apply(self):
        for variable in ["NO_DATABASE", "DATABASE_UNHEALTHY"]:
            with self.subTest(variable=variable):
                result = self.run_script("--apply", "--receipt", str(self.receipt), **{variable: "1"})
                self.assertNotEqual(result.returncode, 0)
                self.assert_no_mutation()

    def test_expected_current_image_drift_is_refused_without_mutation(self):
        result = self.run_script("--expected-backend-image", "sha256:" + "9" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assert_no_mutation()

    def test_database_restart_is_detected_after_apply(self):
        result = self.run_script("--apply", "--receipt", str(self.receipt), DATABASE_RESTART="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("protected_service_changed", result.stderr)
        self.assertFalse(Path(str(self.receipt) + ".applied.json").exists())

    def test_backend_crash_does_not_report_success(self):
        result = self.run_script("--apply", "--receipt", str(self.receipt), CRASH="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("post_apply_backend_not_ready", result.stderr)

    def test_up_failure_retains_recovery_pin_and_preflight_receipt_without_leak(self):
        result = self.run_script("--apply", "--receipt", str(self.receipt), UP_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("secret-runtime-detail", result.stdout + result.stderr)
        self.assertTrue((self.project / "release.yml").exists())
        self.assertTrue(self.receipt.is_file())
        self.assertEqual(len([call for call in self.calls() if "up" in call]), 1)

    def test_existing_override_and_symlink_are_never_overwritten(self):
        pin = self.project / "release.yml"
        pin.symlink_to(self.root / "absent-target")
        result = self.run_script("--apply", "--receipt", str(self.receipt))
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(pin.is_symlink())
        self.assertFalse(self.log.exists())

    def test_existing_receipt_prevents_apply(self):
        self.receipt.write_text("preserve me")
        result = self.run_script("--apply", "--receipt", str(self.receipt))
        self.assertNotEqual(result.returncode, 0)
        self.assert_no_mutation()
        self.assertEqual(self.receipt.read_text(), "preserve me")

    def test_existing_completion_receipt_prevents_apply(self):
        completion = Path(str(self.receipt) + ".applied.json")
        completion.write_text("preserve me")
        result = self.run_script("--apply", "--receipt", str(self.receipt))
        self.assertNotEqual(result.returncode, 0)
        self.assert_no_mutation()

    def test_concurrent_rollout_refused_before_up(self):
        import fcntl
        with (self.project / ".backend-rollout.lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            result = self.run_script("--apply", "--receipt", str(self.receipt))
        self.assertNotEqual(result.returncode, 0)
        self.assert_no_mutation()


if __name__ == "__main__":
    unittest.main()
