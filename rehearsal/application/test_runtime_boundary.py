"""Pure boundaries; importing/running this suite never starts Docker."""

from contextlib import contextmanager, redirect_stdout
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("application_boundary_runtime", HERE / "runner.py")
RUNTIME = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNTIME)


class RuntimeBoundaries(unittest.TestCase):
    def test_default_command_has_no_docker_or_constructor(self):
        with mock.patch.object(RUNTIME, "ApplicationRunner") as constructor, redirect_stdout(io.StringIO()) as output:
            self.assertEqual(RUNTIME.main([]), 0)
        constructor.assert_not_called()
        self.assertEqual(json.loads(output.getvalue()), {"status": "tools_only", "action": "none", "synthetic_only": True})

    def test_actual_container_refuses_network_privilege_image_and_mount_changes(self):
        image, container = "sha256:" + "a" * 64, "b" * 64
        info = {"Id": container, "Image": image,
                "Config": {"Labels": {"com.docker.compose.project": "invented"}, "User": "999:999"},
                "HostConfig": {"NetworkMode": "none", "PortBindings": {}, "Privileged": False,
                               "PidMode": "", "IpcMode": "private", "CapAdd": None, "CapDrop": ["ALL"],
                               "SecurityOpt": ["no-new-privileges:true"], "ReadonlyRootfs": True},
                "Mounts": [{"Type": "volume", "Name": "invented-config", "Destination": "/run/gnomon-test", "RW": True}],
                "NetworkSettings": {"Ports": {}}}
        options = {"project": "invented", "image": image, "network": "none",
                   "mounts": {("volume", "invented-config", "/run/gnomon-test", True)}}
        RUNTIME.validate_container(info, **options)
        changes = [("HostConfig", "NetworkMode", "bridge"), ("HostConfig", "Privileged", True),
                   ("HostConfig", "CapAdd", ["SYS_ADMIN"]), ("HostConfig", "ReadonlyRootfs", False),
                   ("HostConfig", "PortBindings", {"8888/tcp": [{"HostPort": "8888"}]}),
                   ("Config", "User", "0:0")]
        for group, key, value in changes:
            with self.subTest(group=group, key=key):
                changed = copy.deepcopy(info)
                changed[group][key] = value
                with self.assertRaises(RUNTIME.Refused):
                    RUNTIME.validate_container(changed, **options)
        changed = copy.deepcopy(info)
        changed["Mounts"][0]["Type"] = "bind"
        with self.assertRaises(RUNTIME.Refused):
            RUNTIME.validate_container(changed, **options)
        changed = copy.deepcopy(info)
        changed["Image"] = "sha256:" + "c" * 64
        with self.assertRaises(RUNTIME.Refused):
            RUNTIME.validate_container(changed, **options)

    def test_closed_session_and_arbitrary_targets_are_refused_without_command(self):
        runner = object.__new__(RUNTIME.ApplicationRunner)
        runner._active, runner._failed = False, False
        runner.command = mock.Mock()
        with self.assertRaises(RUNTIME.Refused):
            runner.sql("SELECT 1")
        runner._active = True
        for port, path in [(80, "/"), (8888, "https://example.invalid"), (8888, "//example.invalid"), (8888, "/\n")]:
            with self.subTest(port=port, path=path), self.assertRaises(RUNTIME.Refused):
                runner._http(port, path)
        runner.command.assert_not_called()
        for role in ("other", "sha256:" + "a" * 64):
            runner.backend_id = None
            with self.assertRaises(RUNTIME.Refused):
                runner.start_backend(image=role)
        runner.command.assert_not_called()

    def test_operator_dump_endpoint_and_config_are_not_cli_options(self):
        with redirect_stdout(io.StringIO()), mock.patch("sys.stderr", new=io.StringIO()):
            for argument in ("--bundle", "--endpoint", "--config", "--docker-socket", "--image"):
                with self.subTest(argument=argument), self.assertRaises(SystemExit):
                    RUNTIME.main([argument, "invented"])

    def test_down_kernel_tunnels_are_allowed_but_any_up_external_interface_is_refused(self):
        RUNTIME.validate_interface_flags({"lo": 0x9, "tunl0": 0x80, "gre0": 0x80})
        for flags in ({"lo": 0x9, "eth0": 0x1003}, {"lo": 0x8}, {"lo": "0x9"}, {}):
            with self.subTest(flags=flags), self.assertRaises(RUNTIME.Refused):
                RUNTIME.validate_interface_flags(flags)

    def test_exited_backend_fails_readiness_without_waiting_for_api_timeout(self):
        runner = object.__new__(RUNTIME.ApplicationRunner)
        runner._active, runner._failed, runner.backend_id = True, False, None
        runner.image_ids, runner.mock_id, runner.config_volume = {"candidate": "sha256:" + "a" * 64}, "b" * 64, "invented-config"
        runner.restore = mock.Mock(project="invented", docker=["docker"])
        runner.create = mock.Mock(return_value="c" * 64)
        runner.command = mock.Mock(return_value=("", 0))
        runner.inspect = mock.Mock(return_value={"State": {"Running": False}})
        runner.request = mock.Mock()
        with self.assertRaisesRegex(RuntimeError, "application_backend_exited_before_ready"):
            runner.start_backend()
        runner.request.assert_not_called()

    def test_application_connection_budget_is_bound_to_guarded_owned_clone_and_fixed_role(self):
        for changed_marker in (False, True):
            with self.subTest(changed_marker=changed_marker):
                runner = object.__new__(RUNTIME.ApplicationRunner)
                runner.clone_id, runner.report = "invented-clone", {}
                runner.restore = mock.Mock(database="invented-database", postgres_id="a" * 64)
                marker = {"clone_id": "other" if changed_marker else runner.clone_id, "database_name": "invented-database"}
                before = {"connection_limit": 8, "superuser": False, "create_db": False, "create_role": False,
                          "replication": False, "bypass_rls": False}
                after = {**before, "connection_limit": 24}
                runner.session = mock.Mock()
                runner.session.sql.side_effect = [json.dumps(marker), json.dumps(before), "30", json.dumps(after)]
                if changed_marker:
                    with self.assertRaisesRegex(RUNTIME.Refused, "application_clone_marker_mismatch"):
                        runner.configure_connection_budget()
                    runner.restore.sql.assert_not_called()
                else:
                    runner.configure_connection_budget()
                    runner.restore.inspect_container.assert_called_once_with("a" * 64, "postgres")
                    runner.restore.sql.assert_called_once_with("ALTER ROLE rehearsal_owner CONNECTION LIMIT 24", role="rehearsal_admin")
                    self.assertEqual(runner.report['clone_connection_budget']['application_owner_limit'], 24)

    def test_backend_context_verifies_blobs_and_excludes_untracked_config(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "backend").mkdir()
            contents = {"Dockerfile": b"FROM scratch\n", "main.go": b"package main\n"}
            entries = []
            for name, value in contents.items():
                (root / "backend" / name).write_bytes(value)
                digest = hashlib.sha1(b"blob " + str(len(value)).encode() + b"\0" + value).hexdigest()
                entries.append("100644 blob " + digest + "\tbackend/" + name + "\0")
            (root / "backend/config.yaml").write_text("operator-config-must-not-enter-build")
            command = mock.Mock(return_value=("".join(entries), 0))
            evidence = RUNTIME.PROVENANCE.freeze_backend(root, root / "frozen", command, RUNTIME.Refused)
            self.assertEqual(set(evidence["files"]), set(contents))
            self.assertFalse((root / "frozen/config.yaml").exists())
            (root / "backend/main.go").write_text("changed")
            with self.assertRaises(RUNTIME.Refused):
                RUNTIME.PROVENANCE.freeze_backend(root, root / "rejected", command, RUNTIME.Refused)

    def test_callback_failure_and_cleanup_failure_publish_no_success(self):
        for fail_body, fail_cleanup in [(True, False), (False, True)]:
            with self.subTest(body=fail_body, cleanup=fail_cleanup), tempfile.TemporaryDirectory() as temporary:
                output = Path(temporary).resolve()
                output.chmod(0o700)
                runner = RUNTIME.ApplicationRunner(output)
                session = mock.Mock(_failed=False)
                session.run_initdb.return_value = {"review_required": True}
                @contextmanager
                def clone():
                    yield session
                restore = mock.Mock(project="invented-project", clone_id="invented-clone")
                restore.open_clone.side_effect = clone
                runner.prepare = mock.Mock()
                runner.cleanup = mock.Mock(side_effect=RUNTIME.Refused("cleanup_failed") if fail_cleanup else None)
                with mock.patch.object(RUNTIME.FIXTURE, "create_bundle", return_value=output / "bundle"), \
                        mock.patch.object(RUNTIME.RESTORE, "RestoreRunner", return_value=restore), \
                        mock.patch.object(RUNTIME.ORACLE, "read_source_receipt", return_value={"snapshot": {}}), \
                        mock.patch.object(RUNTIME.ORACLE, "capture_snapshot", return_value={}), \
                        mock.patch.object(RUNTIME.ORACLE, "compare_snapshots"):
                    with self.assertRaises((RuntimeError, RUNTIME.Refused)):
                        with runner.open_application():
                            if fail_body:
                                raise RuntimeError("invented")
                runner.cleanup.assert_called_once()
                self.assertFalse((output / "application-summary.json").exists())
                self.assertFalse(runner.report["application_lifecycle_verified"])
                with self.assertRaises(RUNTIME.Refused):
                    with runner.open_application():
                        pass

    def test_failed_image_removal_and_changed_image_ownership_block_cleanup(self):
        for wrong_owner in (False, True):
            with self.subTest(wrong_owner=wrong_owner):
                runner = object.__new__(RUNTIME.ApplicationRunner)
                runner.project, runner.containers, runner._volume_created = "invented", {}, False
                runner.tags = {"candidate": "invented-candidate:local-only"}
                runner.image_ids = {"candidate": "sha256:" + "a" * 64}
                runner.restore = mock.Mock(docker=["docker", "--host", "unix:///invented.sock"])
                runner.journal = mock.Mock()
                calls = []
                def command(arguments, **_options):
                    calls.append(arguments)
                    if "inspect" in arguments:
                        return json.dumps([{"Id": runner.image_ids["candidate"], "Config": {"Labels": {
                            "gnomonitoring.synthetic.project": "other" if wrong_owner else "invented"}}}]), 0
                    if "rm" in arguments:
                        raise RUNTIME.Refused("synthetic_docker_rm_failed")
                    if "image" in arguments:
                        return "owned-image-remains", 0
                    return "", 0
                runner.command = command
                with self.assertRaisesRegex(RUNTIME.Refused, "application_cleanup_unverified"):
                    runner.cleanup()
                runner.journal.assert_called_once()
                self.assertEqual(runner.journal.call_args.args[0], "cleanup_unverified")
                if wrong_owner:
                    self.assertFalse(any("rm" in call for call in calls), "unowned image was deleted")

    def test_diagnostic_capture_failure_still_removes_owned_container(self):
        runner = object.__new__(RUNTIME.ApplicationRunner)
        runner.project = "invented"
        container = "b" * 64
        runner.containers = {container: {"role": "mocks", "image": "sha256:" + "a" * 64,
                                         "network": "none", "mounts": set()}}
        runner.restore = mock.Mock(docker=["docker", "--host", "unix:///invented.sock"])
        runner.inspect = mock.Mock(return_value={})
        runner.journal = mock.Mock()
        calls = []
        def command(arguments, **_options):
            calls.append(arguments)
            if "logs" in arguments:
                raise RUNTIME.Refused("diagnostic_capture_overflow")
            return "", 0
        runner.command = command
        with mock.patch.object(RUNTIME, "validate_container"), self.assertRaisesRegex(RUNTIME.Refused, "diagnostic_capture_overflow"):
            runner._remove_container(container)
        self.assertTrue(any("rm" in call for call in calls))
        self.assertFalse(runner.containers)

    def test_untagged_owned_image_blocks_completed_cleanup(self):
        runner = object.__new__(RUNTIME.ApplicationRunner)
        runner.project, runner.containers, runner.tags, runner._volume_created = "invented", {}, {}, False
        runner.restore = mock.Mock(docker=["docker"])
        runner.journal = mock.Mock()
        calls = []
        def command(arguments, **_options):
            calls.append(arguments)
            return ("untagged-owned-image" if "label=gnomonitoring.synthetic.project=invented" in arguments else ""), 0
        runner.command = command
        with self.assertRaisesRegex(RUNTIME.Refused, "application_cleanup_unverified"):
            runner.cleanup()
        self.assertTrue(any("label=gnomonitoring.synthetic.project=invented" in call for call in calls))

    def test_interrupted_summary_write_never_exposes_completed_path(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            def interrupted_write(path, _content):
                path.write_bytes(b'{"synthetic_only":')
                raise OSError("invented interrupted write")
            with mock.patch.object(RUNTIME.RESTORE, "private_write", side_effect=interrupted_write):
                with self.assertRaises(OSError):
                    RUNTIME.publish_application_summary(output, {"synthetic_only": True})
            self.assertFalse((output / "application-summary.json").exists())
            self.assertFalse((output / ".application-summary.pending").exists())

    def test_stage_journal_failure_and_real_sigterm_do_not_abort_resource_teardown(self):
        for cause in ("journal_io", "sigterm"):
            with self.subTest(cause=cause), tempfile.TemporaryDirectory() as temporary:
                output = Path(temporary).resolve()
                output.chmod(0o700)
                runner = RUNTIME.ApplicationRunner(output)
                session = mock.Mock(_failed=False)
                session.run_initdb.return_value = {"review_required": True}
                @contextmanager
                def clone():
                    yield session
                restore = mock.Mock(project="invented-project", clone_id="invented-clone", docker=["docker"])
                restore.open_clone.side_effect = clone
                if cause == "journal_io":
                    restore._journal.side_effect = OSError("invented journal failure")
                def prepare():
                    runner.containers = {"first": {}, "second": {}}
                runner.prepare = prepare
                removed = []
                def remove(container):
                    removed.append(container)
                    if cause == "sigterm" and len(removed) == 1:
                        os.kill(os.getpid(), signal.SIGTERM)
                runner._remove_container = remove
                runner.command = mock.Mock(return_value=("", 0))
                runner.journal = mock.Mock()
                previous = signal.getsignal(signal.SIGTERM)
                with mock.patch.object(RUNTIME.FIXTURE, "create_bundle", return_value=output / "bundle"), \
                        mock.patch.object(RUNTIME.RESTORE, "RestoreRunner", return_value=restore), \
                        mock.patch.object(RUNTIME.ORACLE, "read_source_receipt", return_value={"snapshot": {}}), \
                        mock.patch.object(RUNTIME.ORACLE, "capture_snapshot", return_value={}), \
                        mock.patch.object(RUNTIME.ORACLE, "compare_snapshots"):
                    with self.assertRaisesRegex(RUNTIME.Refused, "application_cleanup_unverified"):
                        with runner.open_application():
                            pass
                self.assertEqual(set(removed), {"first", "second"})
                self.assertEqual(runner.command.call_count, 4, "final resource inspections were skipped")
                self.assertEqual(signal.getsignal(signal.SIGTERM), previous)
                self.assertFalse((output / "application-summary.json").exists())
                if cause == "sigterm":
                    self.assertTrue(session._failed)


if __name__ == "__main__":
    unittest.main()
