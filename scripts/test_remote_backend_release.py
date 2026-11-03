"""Exercise validated SSH payload data and the fixed remote command boundary."""
from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import build_backend_release_bundle as builder
import remote_backend_release as remote


def request():
    return {"operation": "plan", "image": "ghcr.io/samouraiworld/gnomonitoring-server@sha256:" + "a" * 64,
            "revision": "b" * 40, "backend_container": "c" * 64,
            "expected_backend_image": "sha256:" + "d" * 64, "release_id": "fixture-20261103"}


class RemoteReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.home = Path(self.temporary.name).resolve()
        self.directory = self.home / "gnomonitoringv2"
        self.directory.mkdir()
        self.row = {"project": "gnomonitoringv2", "service": "detect-proposal",
                    "directory": str(self.directory), "files": str(self.directory / "base.yml"),
                    "image": request()["expected_backend_image"], "status": "running"}

    def execute(self, data, row=None):
        def plan(args):
            print(json.dumps({"status": "applied_acceptance_pending" if args.apply else "plan_only",
                              "applied": args.apply, "functional_acceptance": "pending"}))
        with patch.object(remote.Path, "home", return_value=self.home), \
                patch.object(remote.rollout, "inspect_container", return_value=row or self.row), \
                patch.object(remote.rollout, "docker") as docker, \
                patch.object(remote.rollout, "run", side_effect=plan) as run:
            output = io.StringIO()
            with redirect_stdout(output):
                remote.execute(data)
            return json.loads(output.getvalue()), docker.call_args_list, run.call_args.args[0]

    def test_plan_has_no_pull_or_apply_and_retains_all_live_files(self):
        row = {**self.row, "files": self.row["files"] + "," + str(self.directory / "previous.yml")}
        result, calls, args = self.execute(request(), row)
        self.assertEqual(result["transport_operation"], "plan")
        self.assertFalse(args.apply)
        self.assertEqual(calls, [])
        self.assertEqual(args.compose_file, row["files"].split(","))

    def test_pull_is_explicit_and_only_selected_immutable_image(self):
        result, calls, args = self.execute({**request(), "operation": "pull"})
        self.assertEqual([call.args for call in calls], [("pull", request()["image"])])
        self.assertFalse(args.apply)
        self.assertEqual(result["functional_acceptance"], "pending")

    def test_apply_uses_fixed_target_and_private_receipt(self):
        result, calls, args = self.execute({**request(), "operation": "apply"})
        self.assertTrue(args.apply)
        self.assertEqual(calls, [])
        self.assertEqual(args.project, "gnomonitoringv2")
        self.assertEqual(args.override, "backend-release-fixture-20261103.yml")
        self.assertEqual(args.receipt.parent, self.directory)
        self.assertEqual(result["functional_acceptance"], "pending")

    def test_wrong_remote_target_is_refused_before_pull_or_apply(self):
        for key, value in [("project", "other"), ("service", "postgres"),
                           ("directory", "/retired"), ("image", "sha256:" + "0" * 64),
                           ("status", "exited"), ("files", "relative.yml")]:
            with self.subTest(key=key), patch.object(remote.Path, "home", return_value=self.home), \
                    patch.object(remote.rollout, "inspect_container", return_value={**self.row, key: value}), \
                    patch.object(remote.rollout, "docker") as docker, patch.object(remote.rollout, "run") as run:
                with self.assertRaises(remote.rollout.Refused):
                    remote.execute({**request(), "operation": "pull"})
                docker.assert_not_called()
                run.assert_not_called()

    def test_injection_and_unrecognised_fields_are_refused_before_bundle(self):
        for key, value in [("release_id", "x; touch /tmp/unwanted"), ("operation", "up"),
                           ("image", "$(secret)"), ("revision", "HEAD"),
                           ("backend_container", "--all"), ("expected_backend_image", "latest"),
                           ("new_field", "unexpected"), ("release_id", 42)]:
            with self.subTest(key=key), self.assertRaises(remote.rollout.Refused):
                builder.build_bundle({**request(), key: value})

    def test_bundle_executes_in_isolated_python_with_data_round_trip(self):
        sources = {"rollout_backend": "# synthetic module\n",
                   "remote_backend_release": "import json\ndef main(request):\n print(json.dumps(request, sort_keys=True))\n return 0\n"}
        bundle = builder.build_bundle(request(), sources=sources)
        result = subprocess.run([sys.executable, "-I", "-"], input=bundle,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), request())

    def test_bundle_does_not_capture_environment_secrets(self):
        with patch.dict(os.environ, {"SSH_PRIVATE_KEY": "do-not-copy-this-private-value"}):
            bundle = builder.build_bundle(request())
        self.assertNotIn("do-not-copy-this-private-value", bundle)
        self.assertNotIn("SSH_PRIVATE_KEY", bundle)

    def test_bundle_builder_refuses_to_replace_existing_output(self):
        output = self.home / "payload.py"
        output.write_text("preserve")
        result = subprocess.run([sys.executable, "-B", str(Path(builder.__file__)), "--output", str(output)],
                                input=json.dumps(request()), capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(output.read_text(), "preserve")


if __name__ == "__main__":
    unittest.main()
