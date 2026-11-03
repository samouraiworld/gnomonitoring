"""Opt-in real Compose forward/return test; synthetic rows, loopback registry.

The IMAGE pattern alone is replaced inside this test process to accept the
ephemeral loopback registry. Production has no such switch. Every other
provenance, snapshot, command and file check runs unchanged against real Docker.
"""
import argparse
from contextlib import redirect_stdout
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch
import uuid


SCRIPT = Path(__file__).with_name("rollout_backend.py")
REGISTRY = "registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
BASE = "debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3"
POSTGRES = "postgres:16@sha256:9e2cf6287f25c3151f4c9421ccce2b17f3e60061c111a29165e0d2cdd9ff0a97"


def docker(*args, check=True, timeout=180):
    result = subprocess.run(["docker", *args], text=True, capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise AssertionError("Synthetic Docker command failed: " + result.stderr[-3000:])
    return result.stdout.strip()


@unittest.skipUnless(os.environ.get("RUN_SYNTHETIC_BACKEND_ROLLOUT_TESTS") == "1",
                     "explicit disposable Docker rehearsal opt-in required")
class DockerRolloutTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="gnomon-rollout-fixture-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.project = "gnomon-rollout-fixture-" + uuid.uuid4().hex[:12]
        self.registry = self.project + "-registry"
        self.repository = None
        self.refs = []
        self.base = self.root / "compose.yml"
        self.compose = ["compose", "--project-name", self.project,
                        "--project-directory", str(self.root), "-f", str(self.base)]
        # Register cleanup before starting any daemon resource.
        self.addCleanup(self.cleanup_resources)
        spec = importlib.util.spec_from_file_location("synthetic_rollout", SCRIPT)
        self.rollout = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.rollout)

    def cleanup_resources(self):
        if self.base.exists():
            docker(*self.compose, "down", "--volumes", "--remove-orphans")
        docker("rm", "--force", "--volumes", self.registry, check=False)
        for ref in self.refs:
            docker("image", "rm", ref, check=False)
        remaining = docker("ps", "--all", "--quiet", "--filter",
                           "label=com.docker.compose.project=" + self.project)
        self.assertEqual(remaining, "", "exact synthetic project was not cleaned")
        remaining_registry = docker("ps", "--all", "--quiet", "--filter",
                                    "name=^/" + self.registry + "$")
        self.assertEqual(remaining_registry, "", "synthetic registry was not cleaned")

    def start_registry(self):
        # Bind only the daemon host's loopback, including Docker Desktop's VM.
        # Desktop's Mac-side published port cannot be reached via daemon 127.0.0.1.
        address = "127.0.0.1:" + str(30000 + secrets.randbelow(20000))
        docker("run", "--detach", "--name", self.registry, "--network", "host",
               "--env", "REGISTRY_HTTP_ADDR=" + address, REGISTRY)
        self.repository = address + "/" + self.project
        for _ in range(40):
            result = subprocess.run(["docker", "exec", self.registry, "wget", "-q", "-O",
                                     "/dev/null", "http://" + address + "/v2/"],
                                    capture_output=True, timeout=3)
            if result.returncode == 0:
                return
            time.sleep(0.25)
        self.fail("loopback fixture registry did not become ready")

    def build_fixture(self, name, revision):
        tag = self.repository + ":" + name
        self.refs.append(tag)
        (self.root / "Dockerfile").write_text(
            "FROM " + BASE + '\nCMD ["sleep", "86400"]\n')
        docker("build", "--platform", "linux/amd64", "--network", "none", "--tag", tag,
               "--label", "org.opencontainers.image.source=" + self.rollout.SOURCE,
               "--label", "org.opencontainers.image.revision=" + revision, str(self.root))
        docker("push", tag)
        docker("pull", "--platform", "linux/amd64", tag)
        metadata = self.rollout.inspect_image(tag)
        refs = [ref for ref in metadata["digests"] if ref.startswith(self.repository + "@")]
        self.assertEqual(len(refs), 1)
        self.refs.append(refs[0])
        return refs[0], metadata["id"]

    def invoke(self, image, revision, current, previous_image, files, override, apply=False):
        args = argparse.Namespace(project_directory=self.root, project=self.project,
            compose_file=files, backend_container=current, expected_backend_image=previous_image,
            image=image, revision=revision, override=override,
            receipt=self.root / (override + ".receipt.json"), apply=apply)
        output = io.StringIO()
        fixture_pattern = re.compile(re.escape(self.repository) + r"@sha256:[0-9a-f]{64}\Z")
        with patch.object(self.rollout, "IMAGE", fixture_pattern), redirect_stdout(output):
            self.rollout.run(args)
        return json.loads(output.getvalue())

    def test_forward_and_image_return_preserve_real_postgres(self):
        started = time.monotonic()
        self.start_registry()
        old_ref, old_id = self.build_fixture("old", "1" * 40)
        new_ref, new_id = self.build_fixture("new", "2" * 40)
        self.base.write_text(json.dumps({"services": {
            "postgres": {"image": POSTGRES, "platform": "linux/amd64", "network_mode": "none",
                "environment": {"POSTGRES_USER": "fixture", "POSTGRES_DB": "fixture",
                                "POSTGRES_HOST_AUTH_METHOD": "trust"},
                "tmpfs": ["/var/lib/postgresql/data"],
                "healthcheck": {"test": ["CMD", "pg_isready", "-U", "fixture"],
                                "interval": "1s", "timeout": "2s", "retries": 60}},
            "detect-proposal": {"image": old_ref, "platform": "linux/amd64",
                "network_mode": "none", "depends_on": {"postgres": {"condition": "service_healthy"}}}
        }}))
        docker(*self.compose, "up", "--detach", "--wait", "--wait-timeout", "90")
        before = self.rollout.snapshot(self.compose)
        backend = next(row for row in before.values() if row["service"] == "detect-proposal")
        database = next(row for row in before.values() if row["service"] == "postgres")
        docker("exec", database["id"], "psql", "-U", "fixture", "-d", "fixture", "-v",
               "ON_ERROR_STOP=1", "-c", "CREATE TABLE marker(n integer); INSERT INTO marker VALUES (42)")
        plan = self.invoke(new_ref, "2" * 40, backend["id"], old_id, [str(self.base)], "release.yml")
        self.assertFalse(plan["applied"])
        self.assertFalse((self.root / "release.yml").exists())
        self.assertEqual(self.rollout.snapshot(self.compose), before)
        forward = self.invoke(new_ref, "2" * 40, backend["id"], old_id,
                              [str(self.base)], "release.yml", apply=True)
        self.assertTrue(forward["applied"])
        current = next(row for row in forward["after"].values() if row["service"] == "detect-proposal")
        self.assertEqual(current["image"], new_id)
        self.assertEqual(forward["after"][database["id"]], database)
        returned = self.invoke(old_ref, "1" * 40, current["id"], new_id,
                              [str(self.base), str(self.root / "release.yml")], "return.yml", apply=True)
        final_backend = next(row for row in returned["after"].values() if row["service"] == "detect-proposal")
        self.assertEqual(final_backend["image"], old_id)
        self.assertEqual(returned["after"][database["id"]], database)
        marker = docker("exec", database["id"], "psql", "-U", "fixture", "-d", "fixture",
                        "-Atc", "SELECT n FROM marker")
        self.assertEqual(marker, "42")
        for row in returned["after"].values():
            self.assertEqual(docker("inspect", "--format", "{{.HostConfig.NetworkMode}}", row["id"]), "none")
        print(json.dumps({"fixture_only": True, "plan_forward_return": "passed",
                          "postgres_identity_start_image_and_marker": "unchanged",
                          "backend_and_postgres_network": "none", "registry": "loopback_only",
                          "functional_production_acceptance": "pending",
                          "seconds": round(time.monotonic() - started, 2)}))


if __name__ == "__main__":
    unittest.main()
