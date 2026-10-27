"""Test the temporary workflow locally, without SSH, Docker or real secrets."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import textwrap
import unittest


WORKFLOW = Path(__file__).resolve().parents[1] / "workflows" / "main.yml"
BRANCH = "chore/monitoring-readonly-inspection-20261027"
YAML = WORKFLOW.read_text()
CLIENT = textwrap.dedent(YAML.split("        run: |\n", 1)[1])
REMOTE = CLIENT.split("<<'REMOTE_INSPECTION'\n", 1)[1].rsplit("\nREMOTE_INSPECTION", 1)[0]
CONTAINER_ID = "a" * 64


class ReadOnlyInspectionTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.project = self.root / "documented project"
        self.project.mkdir()
        (self.project / "compose.yml").write_text("password: must-not-print\n")
        (self.project / ".env").write_text("SECRET=must-not-print\n")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.runner_temp = self.root / "runner"
        self.runner_temp.mkdir()
        self.env = {
            **os.environ, "PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
            "INSPECTION_TEST_PROJECT": str(self.project),
            "INSPECTION_TEST_LOG": str(self.root / "commands.jsonl"),
            "RUNNER_TEMP": str(self.runner_temp),
        }

    def executable(self, name, source):
        target = self.bin / name
        target.write_text("#!/usr/bin/env python3\n" + textwrap.dedent(source))
        target.chmod(0o755)

    def mocks(self):
        self.executable("git", r'''
import json, os, sys
a = sys.argv[1:]
with open(os.environ["INSPECTION_TEST_LOG"], "a") as f: f.write(json.dumps(["git", *a]) + "\n")
p = os.environ["INSPECTION_TEST_PROJECT"]
if a == ["-C", p, "rev-parse", "--show-toplevel"]: print(p)
elif a == ["-C", p, "rev-parse", "--verify", "HEAD"]: print("1" * 40)
elif a == ["-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-C", p, "status", "--porcelain", "--untracked-files=no"]:
    assert os.environ.get("GIT_OPTIONAL_LOCKS") == "0"
else: raise SystemExit("Unexpected Git command")
''')
        self.executable("docker", r'''
import json, os, sys
a = sys.argv[1:]
with open(os.environ["INSPECTION_TEST_LOG"], "a") as f: f.write(json.dumps(["docker", *a]) + "\n")
p = os.environ["INSPECTION_TEST_PROJECT"]
if a == ["ps", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project.working_dir=" + p]:
    if os.environ.get("INSPECTION_TEST_PS_FAIL"): raise SystemExit(7)
    print(os.environ.get("INSPECTION_TEST_IDS", "a" * 64))
elif len(a) == 6 and a[:4] == ["inspect", "--type", "container", "--format"]:
    assert a[5] == "a" * 64
    assert not any(x in a[4] for x in [".Env", ".Args", ".Cmd", ".Mounts"])
    print(os.environ.get("INSPECTION_TEST_IMAGE_ID", "sha256:" + "b" * 64) if a[4] == "{{.Image}}" else "status=running\nactual_working_directory=" + p)
elif len(a) == 5 and a[:3] == ["image", "inspect", "--format"]:
    assert a[4] == "sha256:" + "b" * 64
    assert ".RepoDigests" in a[3] and "org.opencontainers.image.revision" in a[3]
    assert not any(x in a[3] for x in [".Env", ".Args", ".Cmd", ".Mounts"])
    print("image_revision=128f5bb\nrepo_digests=[]")
else: raise SystemExit("Unexpected or mutating Docker command")
''')

    def remote(self, **extra):
        self.mocks()
        # Adapt only the fixed project path to a temporary fixture. Never
        # override HOME or inspect the developer's actual home/project files.
        script = REMOTE.replace('"$HOME/gnomonitoringv2"', shlex.quote(str(self.project)))
        return subprocess.run(["bash", "-c", script], capture_output=True, text=True,
                              env={**self.env, **extra})

    def calls(self):
        path = Path(self.env["INSPECTION_TEST_LOG"])
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def test_workflow_is_dispatch_only_guarded_and_has_no_third_party_action(self):
        self.assertIn("on:\n  workflow_dispatch:\n", YAML)
        self.assertIn("permissions:\n  contents: read\n", YAML)
        self.assertIn("github.ref == 'refs/heads/" + BRANCH + "'", YAML)
        self.assertIn("github.repository == 'samouraiworld/gnomonitoring'", YAML)
        self.assertIn("timeout-minutes: 2", YAML)
        for forbidden in ["uses:", "inputs:", "  push:", "  pull_request:", "  deploy:"]:
            self.assertNotIn(forbidden, YAML)
        self.assertIn('project_dir=$(cd "$HOME/gnomonitoringv2" && pwd -P)', REMOTE)

    def test_remote_emits_only_scoped_git_and_docker_metadata(self):
        result = self.remote()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("checkout_revision=" + "1" * 40, result.stdout)
        self.assertIn("checkout_tracked_changes=false", result.stdout)
        self.assertIn("image_revision=128f5bb", result.stdout)
        self.assertIn("present_compose_basename=compose.yml", result.stdout)
        self.assertNotIn("must-not-print", result.stdout + result.stderr)
        docker_calls = [c for c in self.calls() if c[0] == "docker"]
        self.assertEqual(len(docker_calls), 4)
        self.assertEqual(docker_calls[0][-1], "label=com.docker.compose.project.working_dir=" + str(self.project))
        # Render the actual format captured from the script with Go's strict
        # template engine. A mock Docker response alone cannot reproduce the
        # production error caused by an absent optional State.Health map key.
        renderer = self.root / "render-inspection-template"
        helper = Path(__file__).with_name("render_inspection_template.go")
        build = subprocess.run(["go", "build", "-o", str(renderer), str(helper)],
                               capture_output=True, text=True,
                               env={**self.env, "GOMAXPROCS": "1", "GOTOOLCHAIN": "local",
                                    "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off"})
        self.assertEqual(build.returncode, 0, build.stderr)
        template = docker_calls[1][5]
        base_state = {"Status": "running", "StartedAt": "2026-10-27T00:00:00Z"}
        for state, expected in [(base_state, "not_configured"),
                                ({**base_state, "Health": {"Status": "healthy"}}, "healthy")]:
            with self.subTest(state=state):
                data = {"Id": CONTAINER_ID, "Name": "/inspection-fixture", "Image": "sha256:" + "b" * 64,
                        "State": state, "Config": {"Labels": {}}}
                result = subprocess.run([str(renderer)], input=json.dumps({"template": template, "data": data}),
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("health=" + expected, result.stdout)

    def test_no_matching_project_stops_without_inspection_or_broader_scan(self):
        result = self.remote(INSPECTION_TEST_IDS="")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len([c for c in self.calls() if c[0] == "docker"]), 1)

    def test_malformed_container_id_never_reaches_inspect(self):
        result = self.remote(INSPECTION_TEST_IDS="--help")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len([c for c in self.calls() if c[0] == "docker"]), 1)

    def test_malformed_image_id_never_reaches_image_inspect(self):
        result = self.remote(INSPECTION_TEST_IMAGE_ID="--help")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(c[0:3] == ["docker", "image", "inspect"] for c in self.calls()))

    def test_docker_failure_is_propagated(self):
        self.assertEqual(self.remote(INSPECTION_TEST_PS_FAIL="1").returncode, 7)

    def mock_ssh(self):
        self.executable("timeout", r'''
import os, sys
assert sys.argv[1] == "60s"
os.execvp(sys.argv[2], sys.argv[2:])
''')
        self.executable("ssh", r'''
import json, os, stat, sys
from pathlib import Path
a = sys.argv[1:]
key = Path(a[a.index("-i") + 1])
assert stat.S_IMODE(key.stat().st_mode) == 0o600
assert key.read_text() == "dummy-test-only-identity\n"
assert all(name not in os.environ for name in ["SSH_PRIVATE_KEY", "SSH_HOST", "SSH_USER"])
for option in ["StrictHostKeyChecking=yes", "UpdateHostKeys=no", "IdentitiesOnly=yes", "IdentityAgent=none", "BatchMode=yes", "ConnectTimeout=10", "HostKeyAlias=gnomonitoring-prod-inspection"]:
    assert option in a
known = Path(next(x.split("=", 1)[1] for x in a if x.startswith("UserKnownHostsFile=")))
assert all(line.split()[0] == "gnomonitoring-prod-inspection" for line in known.read_text().splitlines())
assert len(known.read_text().splitlines()) == 3
assert a[-2:] == ["example.invalid", "bash --noprofile --norc -s"]
assert "docker ps --all --quiet --no-trunc" in sys.stdin.read()
if os.environ.get("INSPECTION_TEST_SSH_FAIL"): raise SystemExit(255)
print("mock-inspection-complete")
''')

    def client(self, **extra):
        self.mock_ssh()
        return subprocess.run(["bash", "-c", CLIENT], capture_output=True, text=True,
                              env={**self.env, "SSH_HOST": "example.invalid",
                                   "SSH_USER": "inspection-user",
                                   "SSH_PRIVATE_KEY": "dummy-test-only-identity", **extra})

    def test_ssh_uses_strict_trust_private_temporary_key_and_cleanup(self):
        result = self.client()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("dummy-test-only-identity", result.stdout + result.stderr)
        self.assertEqual(list(self.runner_temp.iterdir()), [])

    def test_ssh_failure_still_removes_identity(self):
        result = self.client(INSPECTION_TEST_SSH_FAIL="1")
        self.assertEqual(result.returncode, 255, result.stderr)
        self.assertEqual(list(self.runner_temp.iterdir()), [])

    def test_missing_secrets_stop_before_ssh_or_key_creation(self):
        result = self.client(SSH_PRIVATE_KEY="")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(self.runner_temp.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
