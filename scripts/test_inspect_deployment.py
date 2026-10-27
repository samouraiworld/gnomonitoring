"""Exercise the read-only command boundary without a Docker daemon or server."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("inspect-deployment.sh")
CONTAINER = "a" * 64


class InspectionTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.project = self.root / "project with spaces"
        self.project.mkdir()
        (self.project / "production.yml").write_text("name: gnomonitoring\n")
        self.log = self.root / "calls.jsonl"
        fake = self.root / "docker"
        fake.write_text("""#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ['CALL_LOG'], 'a') as f:
    f.write(json.dumps(args) + '\\n')
if args[0] == 'compose' and args[-3:] == ['ps', '--all', '--quiet']:
    if os.environ.get('PS_FAIL'): sys.exit(3)
    print(os.environ.get('IDS', 'a' * 64))
elif args[0] == 'inspect' and args[1] == '--format':
    print('sha256:' + 'b' * 64 if args[2] == '{{.Image}}' else 'status=running')
elif args[:2] == ['image', 'inspect']:
    print('image_revision=128f5bb')
else:
    print('Unexpected or mutating Docker command', file=sys.stderr)
    sys.exit(4)
""")
        fake.chmod(0o755)
        self.env = {**os.environ, "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
                    "CALL_LOG": str(self.log), "POSTGRES_PASSWORD": "must-not-be-printed"}

    def run_script(self, *args, **env):
        return subprocess.run(["bash", str(SCRIPT), *map(str, args)],
                              capture_output=True, text=True, env={**self.env, **env})

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_explicit_compose_and_only_read_only_docker_commands(self):
        result = self.run_script(self.project, "production.yml")
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        self.assertEqual(calls[0], ["compose", "--project-directory", str(self.project),
                                   "-f", str(self.project / "production.yml"),
                                   "ps", "--all", "--quiet"])
        self.assertEqual(len(calls), 4)
        self.assertIn("checkout_revision=unavailable", result.stdout)
        self.assertIn("image_revision=128f5bb", result.stdout)
        self.assertNotIn("must-not-be-printed", result.stdout + result.stderr)
        self.assertNotIn("config", [arg for call in calls for arg in call])

    def test_absolute_compose_path(self):
        path = self.project / "production.yml"
        self.assertEqual(self.run_script(self.project, path).returncode, 0)
        self.assertEqual(self.calls()[0][3], "-f")
        self.assertEqual(self.calls()[0][4], str(path))

    def test_real_templates_handle_optional_health_and_labels_with_strict_lookups(self):
        result = self.run_script(self.project, "production.yml")
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        container_format, image_format = calls[1][2], calls[3][3]
        renderer = self.root / "render-inspection-template"
        build = subprocess.run(
            ["go", "build", "-o", str(renderer), str(SCRIPT.with_name("render_inspection_template.go"))],
            capture_output=True, text=True,
            env={**self.env, "GOMAXPROCS": "1", "GOTOOLCHAIN": "local", "GOWORK": "off",
                 "GOPROXY": "off", "GOSUMDB": "off"})
        self.assertEqual(build.returncode, 0, build.stderr)
        labels = {"org.opencontainers.image.revision": "1" * 40,
                  "org.opencontainers.image.source": "https://github.com/samouraiworld/gnomonitoring",
                  "com.docker.compose.project": "gnomonitoring",
                  "com.docker.compose.service": "backend",
                  "com.docker.compose.project.config_files": str(self.project / "production.yml"),
                  "com.docker.compose.project.working_dir": str(self.project)}
        for label_fields in [{}, {"Labels": None}, {"Labels": {}}, {"Labels": {"unrelated": "value"}},
                             {"Labels": {"org.opencontainers.image.revision": "1" * 40}}, {"Labels": labels}]:
            for health_fields in [{}, {"Health": {"Status": "healthy"}}]:
                with self.subTest(labels=label_fields, health=health_fields):
                    data = {"Id": CONTAINER, "Name": "/inspection-fixture", "Image": "sha256:" + "b" * 64,
                            "Config": {"Image": "test-image", **label_fields},
                            "State": {"Status": "running", "StartedAt": "2026-10-27T00:00:00Z", **health_fields},
                            "RepoDigests": []}
                    outputs = []
                    for fmt in [container_format, image_format]:
                        rendered = subprocess.run(
                            [str(renderer)], input=json.dumps({"template": fmt, "data": data}),
                            capture_output=True, text=True)
                        self.assertEqual(rendered.returncode, 0, rendered.stderr)
                        outputs.append(dict(line.split("=", 1) for line in rendered.stdout.splitlines()))
                    self.assertEqual(outputs[0]["health"], "healthy" if health_fields else "not_configured")
                    self.assertEqual(outputs[1]["repo_digests"], "[]")
                    actual_labels = label_fields.get("Labels") or {}
                    for name, key in [("image_revision", "org.opencontainers.image.revision"),
                                      ("image_source", "org.opencontainers.image.source")]:
                        self.assertEqual(outputs[1][name], actual_labels.get(key, ""))
                    for name, key in [("compose_project", "com.docker.compose.project"),
                                      ("compose_service", "com.docker.compose.service"),
                                      ("actual_compose_files", "com.docker.compose.project.config_files"),
                                      ("actual_working_directory", "com.docker.compose.project.working_dir")]:
                        self.assertEqual(outputs[0][name], actual_labels.get(key, ""))
        # Image metadata can also omit Config entirely. Required container
        # identity/status fields remain strict rather than hiding malformed input.
        for config_fields in [{}, {"Config": None}]:
            with self.subTest(image_config=config_fields):
                rendered = subprocess.run(
                    [str(renderer)], input=json.dumps({"template": image_format,
                        "data": {"Id": "sha256:" + "b" * 64, "RepoDigests": [], **config_fields}}),
                    capture_output=True, text=True)
                self.assertEqual(rendered.returncode, 0, rendered.stderr)
                fields = dict(line.split("=", 1) for line in rendered.stdout.splitlines())
                self.assertEqual(fields["image_revision"], "")
                self.assertEqual(fields["image_source"], "")

    def test_override_files_keep_their_order_and_project_name(self):
        override = self.project / "override with spaces.yml"
        override.write_text("services: {}\n")
        result = self.run_script(self.project, "production.yml", override,
                                 COMPOSE_PROJECT_NAME="existing-name")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls()[0][3:7],
                         ["-f", str(self.project / "production.yml"), "-f", str(override)])

    def test_requires_both_paths_without_contacting_docker(self):
        self.assertEqual(self.run_script().returncode, 2)
        self.assertFalse(self.log.exists())

    def test_missing_compose_without_contacting_docker(self):
        self.assertEqual(self.run_script(self.project, "missing.yml").returncode, 2)
        self.assertFalse(self.log.exists())

    def test_empty_project_is_not_success(self):
        self.assertNotEqual(self.run_script(self.project, "production.yml", IDS="").returncode, 0)
        self.assertEqual(len(self.calls()), 1)

    def test_compose_failure_is_propagated(self):
        self.assertEqual(self.run_script(self.project, "production.yml", PS_FAIL="1").returncode, 3)
        self.assertEqual(len(self.calls()), 1)

    def test_unexpected_compose_output_never_reaches_inspect(self):
        self.assertNotEqual(self.run_script(self.project, "production.yml", IDS="--help").returncode, 0)
        self.assertEqual(len(self.calls()), 1)


if __name__ == "__main__":
    unittest.main()
