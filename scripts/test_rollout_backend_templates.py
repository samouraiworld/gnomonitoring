"""Render real Go templates, including fields absent from Docker inspection."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import rollout_backend as rollout


class TemplateTest(unittest.TestCase):
    def test_optional_health_and_labels_with_strict_go_map_lookup(self):
        with tempfile.TemporaryDirectory() as temporary:
            renderer = Path(temporary) / "renderer"
            build = subprocess.run(["go", "build", "-o", str(renderer),
                str(Path(__file__).with_name("render_inspection_template.go"))],
                capture_output=True, text=True, env={**os.environ,
                "GOCACHE": os.environ.get("GOCACHE", str(Path(temporary) / "cache")),
                "GOTOOLCHAIN": "local", "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off"})
            self.assertEqual(build.returncode, 0, build.stderr)
            for health in [{}, {"Health": None}, {"Health": {"Status": "healthy"}}]:
                with self.subTest(health=health):
                    data = {"Id": "a" * 64, "Image": "sha256:" + "b" * 64,
                        "State": {"Status": "running", "StartedAt": "fixture", **health},
                        "Config": {"Labels": {}}, "RepoDigests": []}
                    result = subprocess.run([str(renderer)], capture_output=True, text=True,
                        input=json.dumps({"template": rollout.CONTAINER_FORMAT, "data": data}))
                    self.assertEqual(result.returncode, 0, result.stderr)
                    parsed = json.loads(result.stdout)
                    self.assertEqual(parsed["health"], "healthy" if health.get("Health") else None)
            for labels in [{}, None, {"org.opencontainers.image.revision": "1" * 40}]:
                with self.subTest(labels=labels):
                    result = subprocess.run([str(renderer)], capture_output=True, text=True,
                        input=json.dumps({"template": rollout.IMAGE_FORMAT,
                        "data": {"Id": "sha256:" + "b" * 64, "RepoDigests": [], "Config": {"Labels": labels}}}))
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(json.loads(result.stdout)["digests"], [])


if __name__ == "__main__":
    unittest.main()
