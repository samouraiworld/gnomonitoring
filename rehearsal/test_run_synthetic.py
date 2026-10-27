import copy
import importlib.util
import hashlib
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("run_synthetic", Path(__file__).with_name("run-synthetic.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class RehearsalBoundaryTests(unittest.TestCase):
    def config(self):
        project = "gnomon-rehearsal-fixture"
        config = {
            "services": {name: {"network_mode": "none", "user": "999:999", "cap_drop": ["ALL"], "image": f"{project}-{name}:local-only"} for name in ["postgres", "initdb"]},
            "volumes": {key: {"name": f"{project}-{suffix}"} for key, suffix in [("pgdata", "pgdata"), ("socket", "socket"), ("private_logs", "private-logs")]},
        }
        config["services"]["postgres"]["environment"] = {"POSTGRES_USER": "rehearsal_admin", "POSTGRES_DB": "postgres", "POSTGRES_PASSWORD": "synthetic", "REHEARSAL_CLONE_ID": "fixture"}
        config["services"]["initdb"]["read_only"] = True
        for name in ["postgres", "initdb"]:
            config["services"][name]["volumes"] = [
                {"type": "volume", "source": "socket", "target": "/run/rehearsal-postgres", "read_only": name == "initdb"},
                {"type": "volume", "source": "private_logs", "target": "/run/rehearsal-private"}]
        config["services"]["postgres"]["volumes"].append({"type": "volume", "source": "pgdata", "target": "/var/lib/postgresql/data"})
        return project, config

    def test_compose_requires_none_network_and_fresh_owned_volumes(self):
        project, valid = self.config()
        MODULE.validate_config(valid, project)
        changes = [
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
                MODULE.validate_config(candidate, project)

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

    def test_untracked_and_ignored_go_files_cannot_change_pinned_database_source(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            path = "backend/internal/database/db_init.go"
            (root / path).parent.mkdir(parents=True)
            content = b"package database\n"
            (root / path).write_bytes(content)
            for helper in MODULE.HELPER_FILES:
                (root / helper).parent.mkdir(parents=True, exist_ok=True)
                (root / helper).write_text("package main\n")
            digest = hashlib.sha1(b"blob " + str(len(content)).encode() + b"\0" + content).hexdigest()
            tree = f"100644 blob {digest}\t{path}\0"
            runner = MODULE.Runner.__new__(MODULE.Runner)
            with patch.object(MODULE, "ROOT", root):
                with patch.object(runner, "command", side_effect=[(tree, 0), (path+"\0", 0), ("", 0), ("", 0)]):
                    self.assertEqual(len(runner.pin_sources()), 5)
                for outputs in [("backend/internal/database/extra.go\0", ""), ("", "backend/internal/database/ignored.go\0")]:
                    with patch.object(runner, "command", side_effect=[(tree, 0), (path+"\0", 0), (outputs[0], 0), (outputs[1], 0)]):
                        with self.assertRaises(MODULE.Refused):
                            runner.pin_sources()
                    self.assertEqual((root / path).read_bytes(), content)
                (root / path).write_text("changed despite index flags\n")
                with patch.object(runner, "command", return_value=(tree, 0)):
                    with self.assertRaises(MODULE.Refused):
                        runner.pin_sources()

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


if __name__ == "__main__":
    unittest.main()
