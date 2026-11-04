import copy
import importlib.util
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import tempfile
import subprocess
import sys
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("run_synthetic", Path(__file__).with_name("run-synthetic.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class RehearsalBoundaryTests(unittest.TestCase):
    def test_command_failure_reports_only_fixed_stage_and_exit_code(self):
        with tempfile.TemporaryDirectory() as raw:
            runner = MODULE.Runner.__new__(MODULE.Runner)
            runner.output = Path(raw)
            runner.environment = os.environ.copy()
            runner.sequence = 0
            runner.stage = "fixture_stage"
            with self.assertRaisesRegex(MODULE.Refused, "^synthetic_step_failed_fixture_stage_exit_7$"):
                runner.command([sys.executable, "-c", "import sys; print('private-detail'); sys.exit(7)"])
            self.assertEqual((runner.output / "01-stdout.private").read_text(), "private-detail\n")
            self.assertEqual((runner.output / "01-stdout.private").stat().st_mode & 0o777, 0o600)

    def test_cleanup_failure_cannot_mask_original_failure(self):
        runner = MODULE.Runner.__new__(MODULE.Runner)
        runner.output = self.CONTEXT.parent
        runner.docker = ["docker", "--host", "unix:///approved/local.sock"]
        runner.compose = runner.docker + ["compose"]
        project, config = self.config()
        runner.project = project
        def command(arguments, **unused):
            if arguments == runner.docker + ["info", "--format", "{{.OSType}}"]:
                return "linux", 0
            if arguments == runner.compose + ["config", "--format", "json"]:
                return json.dumps(config), 0
            raise MODULE.Refused("original_build_failure")
        with patch.object(runner, "pin_sources", return_value={}), patch.object(runner, "fresh_volumes"), \
                patch.object(runner, "command", side_effect=command), \
                patch.object(runner, "cleanup", side_effect=MODULE.Refused("secondary_cleanup_failure")), \
                patch.object(MODULE.sys, "stdout", new_callable=io.StringIO) as output:
            with self.assertRaisesRegex(MODULE.Refused, "^original_build_failure$"):
                runner.execute()
            self.assertEqual(json.loads(output.getvalue()), {"status": "synthetic_cleanup_failed"})

    def test_volume_template_accepts_docker_struct_and_raw_map(self):
        renderer = Path(__file__).with_name("render_volume_template.go")
        for mode in ["struct", "map"]:
            completed = subprocess.run(["go", "run", str(renderer), mode], input=MODULE.VOLUME_TEMPLATE,
                                       text=True, capture_output=True, check=False, timeout=60)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            value = json.loads(completed.stdout)
            MODULE.validate_volume_fence(value, "fixture", "fixture")
        old = MODULE.VOLUME_TEMPLATE.replace("json .Options", 'json (index . "Options")')
        completed = subprocess.run(["go", "run", str(renderer), "struct"], input=old,
                                   text=True, capture_output=True, check=False, timeout=60)
        self.assertNotEqual(completed.returncode, 0)

    CONTEXT = Path("/private/fixture-output/initdb-context")

    def config(self):
        project = "gnomon-rehearsal-fixture"
        config = {
            "services": {name: {"network_mode": "none", "user": "999:999", "cap_drop": ["ALL"], "image": f"{project}-{name}:local-only"} for name in ["postgres", "initdb"]},
            "volumes": {key: {"name": f"{project}-{suffix}"} for key, suffix in [("pgdata", "pgdata"), ("socket", "socket"), ("private_logs", "private-logs")]},
        }
        config["services"]["postgres"]["environment"] = {"POSTGRES_USER": "rehearsal_admin", "POSTGRES_DB": "postgres", "POSTGRES_PASSWORD": "synthetic", "REHEARSAL_CLONE_ID": "fixture"}
        config["services"]["initdb"]["read_only"] = True
        config["services"]["initdb"]["build"] = {"context": str(self.CONTEXT), "dockerfile": str(MODULE.INITDB_DOCKERFILE)}
        for name in ["postgres", "initdb"]:
            config["services"][name]["volumes"] = [
                {"type": "volume", "source": "socket", "target": "/run/rehearsal-postgres", "read_only": name == "initdb"},
                {"type": "volume", "source": "private_logs", "target": "/run/rehearsal-private"}]
        config["services"]["postgres"]["volumes"].append({"type": "volume", "source": "pgdata", "target": "/var/lib/postgresql/data"})
        return project, config

    def test_compose_requires_none_network_and_fresh_owned_volumes(self):
        project, valid = self.config()
        MODULE.validate_config(valid, project, self.CONTEXT)
        changes = [
            lambda c: c["services"]["initdb"].pop("build"),
            lambda c: c["services"]["initdb"]["build"].update(context=str(MODULE.ROOT / "backend")),
            lambda c: c["services"]["initdb"]["build"].update(context="../backend"),
            lambda c: c["services"]["initdb"]["build"].update(dockerfile="Dockerfile"),
            lambda c: c["services"]["initdb"]["build"].update(additional_contexts={"source": str(MODULE.ROOT / "backend")}),
            lambda c: c["services"]["postgres"].update(network_mode="host"),
            lambda c: c["services"]["initdb"].update(ports=["5432:5432"]),
            lambda c: c["services"]["initdb"].update(privileged=True),
            lambda c: c["services"]["initdb"].update(cap_add=["NET_ADMIN"]),
            lambda c: c["services"]["initdb"].update(environment={"TELEGRAM_TOKEN": "must-not-be-forwarded"}),
            lambda c: c["services"]["initdb"].update(env_file=["/production/.env"]),
            lambda c: c["services"]["initdb"].update(read_only=False),
            lambda c: c["volumes"]["pgdata"].update(external=True),
            lambda c: c["volumes"]["pgdata"].update(name="production"),
            lambda c: c["volumes"]["socket"].update(driver_opts={"device": "/production"}),
            lambda c: c["volumes"]["pgdata"].update(driver="remote-plugin"),
            lambda c: c["services"]["postgres"].update(volumes=[{"type": "bind", "source": "/existing/production/pgdata", "target": "/var/lib/postgresql/data"}]),
            lambda c: c["services"]["postgres"]["volumes"][0].update(source="existing-production-volume"),
        ]
        for change in changes:
            candidate = copy.deepcopy(valid)
            change(candidate)
            with self.assertRaises(MODULE.Refused):
                MODULE.validate_config(candidate, project, self.CONTEXT)

    def test_actual_container_requires_same_fence(self):
        project = "gnomon-rehearsal-fixture"
        safe = {"network_mode": "none", "ports": {"5432/tcp": None}, "privileged": False, "pid_mode": "", "user": "999:999", "cap_drop": ["ALL"], "security_opt": ["no-new-privileges:true"],
                "mounts": [{"Type": "volume", "Name": project + "-socket"}]}
        MODULE.validate_container_fence(safe, project)
        for extra in [{"network_mode": "bridge"}, {"ports": {"5432/tcp": [{"HostPort": "5432"}]}}, {"pid_mode": "host"},
                      {"mounts": [{"Type": "volume", "Name": "existing-data"}]}, {"mounts": [{"Type": "bind", "Source": "/var/run/docker.sock"}]}]:
            with self.assertRaises(MODULE.Refused):
                MODULE.validate_container_fence({**safe, **extra}, project)

    def test_actual_volume_cannot_use_a_remote_driver_or_options(self):
        project = "gnomon-rehearsal-fixture"
        name = project + "-pgdata"
        safe = {"Name": name, "Driver": "local", "Options": None, "Scope": "local", "Labels": {"com.docker.compose.project": project}}
        MODULE.validate_volume_fence(safe, name, project)
        for extra in [{"Driver": "remote-plugin"}, {"Options": {"type": "nfs", "device": ":/production"}}, {"Scope": "global"}, {"Labels": {}}, {"Name": "production"}]:
            with self.assertRaises(MODULE.Refused):
                MODULE.validate_volume_fence({**safe, **extra}, name, project)

    def test_remote_docker_context_is_refused_before_any_command(self):
        for key, value in [("DOCKER_HOST", "ssh://production"), ("DOCKER_CONTEXT", "production"), ("BUILDKIT_HOST", "tcp://remote:1234"), ("BUILDX_BUILDER", "remote")]:
            with patch.dict(os.environ, {key: value}), patch.object(MODULE.subprocess, "run") as command:
                with self.assertRaises(MODULE.Refused):
                    MODULE.Runner(Path("/unused"))
                command.assert_not_called()

    def test_output_must_be_empty_private_and_owned(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw).resolve()
            os.chmod(directory, 0o700)
            self.assertEqual(MODULE.check_output_directory(str(directory)), directory)
            (directory / "existing").write_text("not disposable")
            with self.assertRaises(MODULE.Refused):
                MODULE.check_output_directory(str(directory))

    def test_arbitrary_docker_socket_override_is_refused(self):
        with self.assertRaises(MODULE.Refused):
            MODULE.approved_socket("/tmp/forwarded-production.sock")

    def test_unpinned_command_and_existing_volume_are_refused(self):
        runner = MODULE.Runner.__new__(MODULE.Runner)
        runner.docker = ["docker", "--host", "unix:///approved/local.sock"]
        runner.project = "gnomon-rehearsal-fixture"
        with self.assertRaises(MODULE.Refused):
            runner.command(["docker", "info"])
        with patch.object(runner, "command", return_value=("gnomon-rehearsal-fixture-pgdata\n", 0)) as command:
            with self.assertRaises(MODULE.Refused):
                runner.fresh_volumes()
            self.assertEqual(command.call_count, 1)
            self.assertEqual(command.call_args.args[0][:3], runner.docker)

    def test_default_does_not_instantiate_runner(self):
        with patch.object(MODULE.sys, "argv", ["run-synthetic.py"]), patch.object(MODULE, "Runner") as runner:
            self.assertEqual(MODULE.main(), 0)
            runner.assert_not_called()


class PinnedBuildContextTests(unittest.TestCase):
    """The InitDB build context is frozen from BASE's Git objects, not the checkout."""

    BASELINE = {
        "backend/internal/database/db_init.go": b"package database\n",
        "backend/fixture.bin": bytes(range(256)) + b"\n\n",
        "backend/same.go": b"package same\n",
        "backend/copy.go": b"package same\n",
    }

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.environment = {"PATH": os.environ["PATH"], "HOME": str(self.root), "GIT_CONFIG_NOSYSTEM": "1"}
        self.git("init", "-q")
        for path, content in self.BASELINE.items():
            (self.root / path).parent.mkdir(parents=True, exist_ok=True)
            (self.root / path).write_bytes(content)
        self.git("add", "backend")
        self.git("commit", "-q", "-m", "invented baseline")
        self.base = self.git("rev-parse", "HEAD").strip()
        # Diverge the checkout like a later revision: a committed backend change,
        # a new tracked file, an untracked and an ignored source, the helpers.
        (self.root / ".gitignore").write_text("backend/internal/database/ignored.go\n")
        (self.root / "backend/internal/database/db_init.go").write_bytes(b"package database // later revision\n")
        (self.root / "backend/internal/database/later.go").write_bytes(b"package database\n")
        for helper in MODULE.HELPER_FILES:
            (self.root / helper).parent.mkdir(parents=True, exist_ok=True)
            (self.root / helper).write_bytes(b"package main\n")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "invented later revision")
        (self.root / "backend/internal/database/extra.go").write_bytes(b"package database // untracked\n")
        (self.root / "backend/internal/database/ignored.go").write_bytes(b"package database // ignored\n")
        self.checkout = {path: path_bytes for path, path_bytes in self.snapshot(self.root / "backend").items()}
        pins = {"ROOT": self.root, "BASE": self.base,
                "DB_SOURCE_SHA": hashlib.sha256(self.BASELINE["backend/internal/database/db_init.go"]).hexdigest()}
        for name, value in pins.items():
            patcher = patch.object(MODULE, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def git(self, *arguments):
        completed = subprocess.run(["git", "-c", "user.name=fixture", "-c", "user.email=fixture@invalid", *arguments],
                                   cwd=self.root, env=self.environment, capture_output=True, check=True, timeout=60)
        return completed.stdout.decode()

    @staticmethod
    def snapshot(directory):
        return {file.relative_to(directory).as_posix(): file.read_bytes() for file in directory.rglob("*") if not file.is_dir()}

    def runner(self):
        self.outputs = getattr(self, "outputs", 0) + 1
        output = self.root / f"output-{self.outputs}"
        output.mkdir(mode=0o700)
        runner = MODULE.Runner.__new__(MODULE.Runner)
        runner.output = output
        runner.environment = self.environment
        runner.sequence = 0
        runner.stage = "pin_fixture"
        runner.docker = ["docker", "--host", "unix:///approved/local.sock"]
        return runner

    def assert_refused(self, code, runner=None):
        runner = runner or self.runner()
        with self.assertRaisesRegex(MODULE.Refused, "^" + code + "$"):
            runner.pin_sources()
        self.assertEqual(self.snapshot(self.root / "backend"), self.checkout, "the checkout must never be written")

    def test_frozen_context_holds_exactly_base_objects_and_helpers(self):
        runner = self.runner()
        helpers = runner.pin_sources()
        self.assertEqual(helpers, {path: hashlib.sha256(b"package main\n").hexdigest() for path in sorted(MODULE.HELPER_FILES)})
        expected = {path.removeprefix("backend/"): content for path, content in self.BASELINE.items()}
        expected.update({path.removeprefix("backend/"): b"package main\n" for path in MODULE.HELPER_FILES})
        # Later, tracked-new, untracked and ignored checkout bytes never reach the build.
        self.assertEqual(self.snapshot(runner.initdb_context), expected)
        self.assertEqual(stat.S_IMODE(runner.initdb_context.stat().st_mode), 0o700)
        for file in runner.initdb_context.rglob("*"):
            if not file.is_dir():
                self.assertEqual(stat.S_IMODE(file.stat().st_mode), 0o600)
        self.assertEqual(self.snapshot(self.root / "backend"), self.checkout, "the checkout must never be written")

    def test_object_bytes_not_matching_their_pinned_digest_are_refused(self):
        with patch.object(MODULE, "read_pinned_blobs", side_effect=lambda digests, environment: {digest: b"tampered\n" for digest in digests}):
            self.assert_refused("baseline_build_context_file_changed")

    def test_existing_or_contaminated_context_is_refused(self):
        runner = self.runner()
        runner.initdb_context.mkdir(mode=0o700)
        self.assert_refused("unexpected_tracked_untracked_or_ignored_build_file", runner)
        original = MODULE.write_context_file
        def contaminate(context, path, content, mode):
            original(context, path, content, mode)
            injected = context / "injected.go"
            if not injected.exists():
                injected.write_bytes(b"package database // injected\n")
        with patch.object(MODULE, "write_context_file", side_effect=contaminate):
            self.assert_refused("unexpected_tracked_untracked_or_ignored_build_file")
        def corrupt(context, path, content, mode):
            original(context, path, content + (b"// changed after freeze\n" if path.endswith("same.go") else b""), mode)
        with patch.object(MODULE, "write_context_file", side_effect=corrupt):
            self.assert_refused("baseline_build_context_file_changed")

    def test_database_source_pin_is_checked_on_the_frozen_context(self):
        with patch.object(MODULE, "DB_SOURCE_SHA", "0" * 64):
            self.assert_refused("database_source_pin_mismatch")

    def test_helper_manifest_must_be_exact(self):
        helper = self.root / sorted(MODULE.HELPER_FILES)[0]
        extra = self.root / "backend/cmd/rehearse-initdb/extra.go"
        extra.write_bytes(b"package main\n")
        self.checkout = self.snapshot(self.root / "backend")
        self.assert_refused("explicit_helper_source_manifest_required")
        extra.unlink()
        helper.unlink()
        self.checkout = self.snapshot(self.root / "backend")
        self.assert_refused("explicit_helper_source_manifest_required")
        helper.symlink_to(self.root / "backend/same.go")
        self.checkout = self.snapshot(self.root / "backend")
        self.assert_refused("explicit_helper_source_manifest_required")

    def test_unsupported_baseline_entries_are_refused(self):
        digest = MODULE.git_blob_digest(b"package main\n")
        for entry in [f"120000 blob {digest}\tbackend/link", f"160000 commit {digest}\tbackend/module",
                      f"100644 blob {digest}\tbackend/../escape.go", f"100644 blob {digest}\t" + sorted(MODULE.HELPER_FILES)[0]]:
            runner = self.runner()
            with patch.object(runner, "command", return_value=(entry + "\0", 0)):
                self.assert_refused("unsupported_baseline_build_context_entry", runner)

    def test_blob_reader_returns_exact_bytes_and_refuses_missing_objects(self):
        digests = [MODULE.git_blob_digest(content) for content in self.BASELINE.values()]
        blobs = MODULE.read_pinned_blobs(digests, self.environment)
        self.assertEqual(blobs, {MODULE.git_blob_digest(content): content for content in self.BASELINE.values()})
        for requested in [["0" * 40], ["HEAD:backend/same.go"], [digests[0], "f" * 40]]:
            with self.assertRaisesRegex(MODULE.Refused, "^pinned_source_objects_unavailable$"):
                MODULE.read_pinned_blobs(requested, self.environment)


if __name__ == "__main__":
    unittest.main()
