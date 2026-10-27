#!/usr/bin/env python3
"""Run the unchanged backend with generated fixtures in an owned offline session."""

import argparse
from contextlib import contextmanager
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import signal
import sys
import tarfile
import time

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


RESTORE = load("application_restore", HERE.parent / "restore/runner.py")
FIXTURE = load("application_fixture", HERE.parent / "restore/synthetic_bundle.py")
ORACLE = load("application_oracle", HERE.parent / "restore/synthetic_snapshot.py")
PROVENANCE = load("application_provenance", HERE / "provenance.py")
Refused = RESTORE.Refused
ID = re.compile(r"[0-9a-f]{64}")
IMAGE = re.compile(r"sha256:[0-9a-f]{64}")
CHAINS = ("synthetic-chain", "synthetic-peer-chain")


def mkdir(parent, name):
    path = parent / name
    path.mkdir(mode=0o700)
    return path


def publish_application_summary(output, report):
    pending, final = output / ".application-summary.pending", output / "application-summary.json"
    try:
        RESTORE.private_write(pending, (json.dumps(report, indent=2) + "\n").encode())
        RESTORE.sync_directory(output)
        os.replace(pending, final)
        RESTORE.sync_directory(output)
    except BaseException:
        pending.unlink(missing_ok=True)
        final.unlink(missing_ok=True)
        raise


def validate_container(info, *, project, image, network, mounts):
    config, host = info["Config"], info["HostConfig"]
    actual_mounts = {(m.get("Type"), m.get("Name"), m.get("Destination"), m.get("RW"))
                     for m in info.get("Mounts", []) if m.get("Type") != "tmpfs"}
    if (not ID.fullmatch(info.get("Id", "")) or info.get("Image") != image
            or config.get("Labels", {}).get("com.docker.compose.project") != project
            or host.get("NetworkMode") != network or host.get("PortBindings")
            or host.get("Privileged") or host.get("PidMode") not in {"", None}
            or host.get("IpcMode") not in {"private", ""} or config.get("User") != "999:999"
            or host.get("CapAdd") or set(host.get("CapDrop") or []) != {"ALL"}
            or not {"no-new-privileges", "no-new-privileges:true"}.intersection(host.get("SecurityOpt") or [])
            or not host.get("ReadonlyRootfs") or actual_mounts != mounts
            or any(m.get("Type") not in {"volume", "tmpfs"} for m in info.get("Mounts", []))
            or any(value for value in info.get("NetworkSettings", {}).get("Ports", {}).values())):
        raise Refused("application_container_fence_mismatch")


def validate_interface_flags(flags):
    if (not isinstance(flags, dict) or not flags or any(type(value) is not int or value < 0 for value in flags.values())
            or {name for name, value in flags.items() if value & 1} != {"lo"}):
        raise Refused("application_only_loopback_may_be_up")


class ApplicationRunner:
    def __init__(self, output):
        RESTORE.FENCES.reject_remote_context(os.environ)
        self.output = RESTORE.private_directory(output, empty=True)
        self._used = False
        self._active = False
        self._failed = False
        self.report = {"synthetic_only": True, "application_lifecycle_verified": False,
                       "production_data_used": False, "cross_version_compatibility_verified": False,
                       "production_rollback_verified": False, "image_transitions": []}
        self.containers = {}
        self.image_ids = {}
        self._volume_created = False
        self.backend_id = None
        self.mock_id = None

    def command(self, arguments, **kwargs):
        return self.restore.command(arguments, **kwargs)

    def inspect(self, container):
        return json.loads(self.command(self.restore.docker + ["inspect", container])[0])[0]

    def journal(self, event, **fields):
        path = self.output / "application-journal.private.jsonl"
        with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND | os.O_NOFOLLOW, 0o600), "ab") as handle:
            handle.write((json.dumps({"event": event, "project": self.project, **fields}) + "\n").encode())
            handle.flush()
            os.fsync(handle.fileno())

    def _check(self):
        if not self._active or self._failed:
            raise Refused("application_session_closed_or_failed")

    @contextmanager
    def open_application(self):
        if self._used:
            raise Refused("application_runner_single_use")
        self._used = True
        bundle = FIXTURE.create_bundle(mkdir(self.output, "invented-source"), capture_source=True)
        self.restore = RESTORE.RestoreRunner(bundle, mkdir(self.output, "restore"), timeout_seconds=180)
        self.project = self.restore.project + "-app"
        self.clone_id = self.restore.clone_id
        self.config_volume = self.project + "-config"
        self.report["project"] = self.project
        self.report["clone_id"] = self.clone_id
        try:
            with self.restore.open_clone() as session:
                self.session = session
                source = ORACLE.read_source_receipt(bundle)
                ORACLE.compare_snapshots(source["snapshot"], ORACLE.capture_snapshot(session.sql))
                self.report["synthetic_source_restoration_equal"] = True
                phase = session.run_initdb()
                self.report["fixture_initdb_review_required"] = phase["review_required"]
                self._active = True
                try:
                    self.prepare()
                    yield self
                    if self._failed:
                        raise Refused("application_session_failed")
                except BaseException:
                    self._failed = True
                    raise
                finally:
                    self._active = False
                    self.cleanup()
            if self.session._failed:
                raise Refused("application_restore_session_failed")
            self.report["owned_resources_cleanup_verified"] = True
            self.report["application_lifecycle_verified"] = bool(self.report.get("backend_started"))
            publish_application_summary(self.output, self.report)
        except BaseException:
            self._failed = True
            self.report["application_lifecycle_verified"] = False
            (self.output / "application-summary.json").unlink(missing_ok=True)
            raise

    def prepare(self):
        docker = self.restore.docker
        self.configure_connection_budget()
        self.restore.stage = "application_build_context"
        context = self.output / "backend-context"
        provenance = PROVENANCE.freeze_backend(ROOT, context, self.command, Refused)
        self.report["backend_source"] = provenance
        tags = {role: self.project + "-" + role + ":local-only" for role in ("baseline", "candidate", "mocks")}
        self.tags = tags
        self.command(docker + ["build", "--label", "gnomonitoring.synthetic.role=baseline", "--label", "gnomonitoring.synthetic.project=" + self.project, "--tag", tags["baseline"], str(context)], timeout=1200)
        self.image_ids["baseline"] = self.command(docker + ["image", "inspect", "--format", "{{.Id}}", tags["baseline"]])[0].strip()
        # Same verified binary, distinct owned image metadata: proves ID selection,
        # not compatibility between different application revisions.
        derived = mkdir(self.output, "candidate-context")
        RESTORE.private_write(derived / "Dockerfile", ("FROM " + tags["baseline"] + "\nLABEL gnomonitoring.synthetic.role=candidate\n").encode())
        self.command(docker + ["build", "--pull=false", "--tag", tags["candidate"], str(derived)], timeout=120)
        mock_context = mkdir(self.output, "mock-context")
        mock_sources = {}
        for name in ("go.mod", "go.sum"):
            target = mock_context / "backend" / name
            target.parent.mkdir(mode=0o700, exist_ok=True)
            RESTORE.private_write(target, (context / name).read_bytes())
            mock_sources["backend/" + name] = hashlib.sha256(target.read_bytes()).hexdigest()
        for name in ("main.go", "Dockerfile"):
            source = HERE / "mock_services" / name
            if source.is_symlink() or not source.is_file():
                raise Refused("mock_source_regular_file_required")
            target = mock_context / "rehearsal/application/mock_services" / name
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            RESTORE.private_write(target, source.read_bytes())
            mock_sources["rehearsal/application/mock_services/" + name] = hashlib.sha256(target.read_bytes()).hexdigest()
        self.report["mock_sources_sha256"] = mock_sources
        self.command(docker + ["build", "--file", str(mock_context / "rehearsal/application/mock_services/Dockerfile"), "--label", "gnomonitoring.synthetic.project=" + self.project, "--tag", tags["mocks"], str(mock_context)], timeout=1200)
        for role in ("candidate", "mocks"):
            self.image_ids[role] = self.command(docker + ["image", "inspect", "--format", "{{.Id}}", tags[role]])[0].strip()
        if not all(IMAGE.fullmatch(value) for value in self.image_ids.values()) or self.image_ids["baseline"] == self.image_ids["candidate"]:
            raise Refused("application_image_identity_invalid")
        self.report["image_ids"] = copy.deepcopy(self.image_ids)
        self.report["image_platforms"] = {}
        image_info = {}
        for role, image in self.image_ids.items():
            info = json.loads(self.command(docker + ["image", "inspect", image])[0])[0]
            image_info[role] = info
            self.report["image_platforms"][role] = {"os": info["Os"], "architecture": info["Architecture"]}
        if (image_info["baseline"]["RootFS"] != image_info["candidate"]["RootFS"]
                or any(image_info["baseline"]["Config"].get(field) != image_info["candidate"]["Config"].get(field)
                       for field in ("Entrypoint", "Cmd", "WorkingDir", "Env"))):
            raise Refused("candidate_identical_binary_fence_mismatch")
        self.report["candidate_rootfs_equals_baseline"] = True
        existing = self.command(docker + ["volume", "ls", "--quiet", "--filter", "name=^" + self.config_volume + "$"])[0]
        if existing.strip():
            raise Refused("application_existing_volume_refused")
        self.journal("volume_planned", name=self.config_volume)
        self._volume_created = True
        self.command(docker + ["volume", "create", "--label", "com.docker.compose.project=" + self.project, self.config_volume])
        volume = json.loads(self.command(docker + ["volume", "inspect", self.config_volume])[0])[0]
        if volume["Driver"] != "local" or volume.get("Options") or volume.get("Labels", {}).get("com.docker.compose.project") != self.project:
            raise Refused("application_config_volume_fence_mismatch")
        self.mock_id = self.create("mocks", self.image_ids["mocks"], "none", [(self.config_volume, "/run/gnomon-test", True)])
        self.command(docker + ["start", self.mock_id])
        self.wait_for(lambda: self.mock_request("/health"), timeout=30)
        self.command(docker + ["exec", self.mock_id, "/usr/local/bin/synthetic-mocks", "self-test"], timeout=60)
        self.report["protocol_contracts_verified"] = True
        # JSON is a YAML subset; no host configuration is read or mounted.
        config = {"backend_port": "8888", "metrics_port": 8989, "dev_mode": False,
                  "allow_origin": "https://synthetic.invalid", "auth_provider": "clerk",
                  "token_telegram_validator": "900001:synthetic-validator", "token_telegram_govdao": "900002:synthetic-govdao",
                  "default_chain": CHAINS[0], "rate_limit_per_second": 100, "rate_limit_burst": 200,
                  "database": {"host": RESTORE.SOCKET, "port": 5432, "user": "rehearsal_owner", "password": "synthetic-clone-only", "dbname": self.restore.database, "sslmode": "disable"},
                  "chains": {chain: {"enabled": True, "rpc_endpoints": ["http://127.0.0.1:" + str(26657 + index)],
                             "graphqls": ["http://127.0.0.1:8080/graphql/" + chain],
                             "gnowebs": ["https://synthetic.invalid/" + chain]} for index, chain in enumerate(CHAINS)}}
        self.copy_config(config)
        self.assert_no_external_network()

    def configure_connection_budget(self):
        # The generic restore remains limited to eight owner connections. The
        # unchanged full backend owns a 20-connection pool, so this new disposable
        # application clone needs a bounded allowance plus private probes.
        self.restore.inspect_container(self.restore.postgres_id, "postgres")
        marker = json.loads(self.session.sql("SELECT json_build_object('clone_id',clone_id,'database_name',database_name) FROM rehearsal_guard.clone_marker"))
        if marker != {"clone_id": self.clone_id, "database_name": self.restore.database}:
            raise Refused("application_clone_marker_mismatch")
        query = """SELECT json_build_object('connection_limit',rolconnlimit,'superuser',rolsuper,
'create_db',rolcreatedb,'create_role',rolcreaterole,'replication',rolreplication,'bypass_rls',rolbypassrls)
FROM pg_roles WHERE rolname='rehearsal_owner'"""
        expected = {"connection_limit": 8, "superuser": False, "create_db": False, "create_role": False,
                    "replication": False, "bypass_rls": False}
        if json.loads(self.session.sql(query)) != expected or self.session.sql("SHOW max_connections").strip() != "30":
            raise Refused("application_initial_connection_budget_mismatch")
        self.restore.sql("ALTER ROLE rehearsal_owner CONNECTION LIMIT 24", role="rehearsal_admin")
        expected["connection_limit"] = 24
        if json.loads(self.session.sql(query)) != expected:
            raise Refused("application_role_privileges_or_budget_mismatch")
        self.report["clone_connection_budget"] = {"generic_restore_owner_limit": 8, "application_owner_limit": 24,
                                                   "backend_pool_max_open": 20, "server_max_connections": 30}

    def create(self, role, image, network, mounts, arguments=()):
        name = self.project + "-" + role
        self.journal("container_planned", name=name, role=role, image=image)
        command = self.restore.docker + ["create", "--name", name, "--label", "com.docker.compose.project=" + self.project,
                  "--network", network, "--user", "999:999", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                  "--pids-limit", "128", "--memory", "512m", "--cpus", "2", "--tmpfs", "/tmp:uid=999,gid=999,mode=0700"]
        for volume, path, writable in mounts:
            command += ["--mount", "type=volume,src=" + volume + ",dst=" + path + ("" if writable else ",readonly")]
        if role == "backend":
            command += ["--workdir", "/run/gnomon-test", "--entrypoint", "/app/server",
                        "--env", "HTTPS_PROXY=http://127.0.0.1:8443", "--env", "NO_PROXY=127.0.0.1,localhost",
                        "--env", "SSL_CERT_FILE=/run/gnomon-test/ca.crt"]
        container = self.command(command + [image, *arguments])[0].strip()
        if not ID.fullmatch(container):
            raise Refused("application_container_id_invalid")
        self.containers[container] = {"role": role, "image": image, "network": network,
                                     "mounts": {("volume", volume, path, writable) for volume, path, writable in mounts}}
        self.journal("container_identified", container_id=container, role=role)
        validate_container(self.inspect(container), project=self.project, **{key: value for key, value in self.containers[container].items() if key != "role"})
        return container

    def copy_config(self, config):
        archive = self.output / "config.tar.private"
        data = (json.dumps(config) + "\n").encode()
        with archive.open("xb") as handle:
            archive.chmod(0o600)
            with tarfile.open(fileobj=handle, mode="w") as tar:
                entry = tarfile.TarInfo("config.yaml")
                entry.size, entry.uid, entry.gid, entry.mode = len(data), 999, 999, 0o400
                tar.addfile(entry, io.BytesIO(data))
        try:
            with archive.open("rb") as handle:
                self.command(self.restore.docker + ["cp", "-a", "-", self.mock_id + ":/run/gnomon-test"], input_file=handle)
        finally:
            archive.unlink(missing_ok=True)

    def sql(self, query):
        self._check()
        return self.session.sql(query)

    def _http(self, port, path, body=None):
        self._check()
        if port not in {8888, 8989, 18080} or not isinstance(path, str) or not path.startswith("/") or path.startswith("//") or "\n" in path:
            raise Refused("application_probe_target_refused")
        script = """import json,sys,urllib.request,urllib.error
url='http://127.0.0.1:'+sys.argv[1]+sys.argv[2]
data=None if sys.argv[3]=='null' else sys.argv[3].encode()
r=urllib.request.Request(url,data=data,headers={'Content-Type':'application/json'})
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
try:
 response=opener.open(r,timeout=5)
except urllib.error.HTTPError as error:
 response=error
raw=response.read(1048577)
if len(raw)>1048576: raise RuntimeError('response_budget')
text=raw.decode()
try: body=json.loads(text)
except ValueError: body=text
print(json.dumps({'status':response.status,'headers':dict(response.headers),'body':body}))
"""
        response = self.command(self.restore.docker + ["exec", self.mock_id, "python3", "-c", script, str(port), path, json.dumps(body)], timeout=15)[0]
        return json.loads(response)

    def request_response(self, path, port=8888):
        if port not in {8888, 8989}:
            raise Refused("application_api_port_refused")
        return self._http(port, path)

    def request(self, path):
        response = self.request_response(path)
        if response["status"] != 200:
            raise Refused("application_api_status_unexpected")
        return response["body"]

    def metrics_text(self):
        return self.request_response("/metrics", port=8989)["body"]

    def backend_log_text(self):
        self._check()
        if not self.backend_id:
            raise Refused("application_backend_not_running")
        # Docker multiplexes backend stderr into its own stderr. Read both only
        # from the bounded private captures, never into a public tool output.
        self.command(self.restore.docker + ["logs", "--tail", "1000", self.backend_id])
        prefix = f"{self.restore.sequence:03d}"
        return "".join((self.restore.output / (prefix + "-" + stream + ".private")).read_text()
                       for stream in ("stdout", "stderr"))

    def mock_request(self, path, body=None):
        if path not in {"/health", "/state", "/control"}:
            raise Refused("application_mock_control_path_refused")
        response = self._http(18080, path, body)
        if response["status"] != 200:
            raise Refused("application_mock_control_failed")
        return response["body"]

    def wait_for(self, callback, timeout=60, interval=0.5):
        if not 0 < timeout <= 180 or not 0 < interval <= 5:
            raise Refused("application_wait_budget_invalid")
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                result = callback()
                if result:
                    return result
            except Refused:
                pass
            time.sleep(interval)
        raise Refused("application_wait_timed_out")

    def start_backend(self, image="candidate", arguments=()):
        self._check()
        if self.backend_id or image not in {"baseline", "candidate"} or list(arguments) not in ([], ["./server"]):
            raise Refused("application_backend_selection_refused")
        mounts = [(self.restore.project + "-socket", RESTORE.SOCKET, False), (self.config_volume, "/run/gnomon-test", False)]
        self.backend_id = self.create("backend", self.image_ids[image], "container:" + self.mock_id, mounts, arguments)
        self.command(self.restore.docker + ["start", self.backend_id])
        def ready():
            if not self.inspect(self.backend_id)["State"]["Running"]:
                raise RuntimeError("application_backend_exited_before_ready")
            return self.request("/info")
        self.wait_for(ready, timeout=60)
        self.report["backend_started"] = True
        self.report["image_transitions"].append({"role": image, "image_id": self.inspect(self.backend_id)["Image"], "arguments": list(arguments)})
        return self.backend_id

    def stop_backend(self):
        if self.backend_id:
            self.command(self.restore.docker + ["stop", "--time", "10", self.backend_id], timeout=20)
            self._remove_container(self.backend_id)
            self.backend_id = None
            active = int(self.session.sql("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND usename='rehearsal_owner'").strip())
            if active:
                raise Refused("application_database_connections_remain")

    def restart_backend(self, image="candidate", arguments=()):
        self.stop_backend()
        return self.start_backend(image, arguments)

    def assert_no_external_network(self):
        self._check()
        info = self.inspect(self.mock_id)
        validate_container(info, project=self.project, **{key: value for key, value in self.containers[self.mock_id].items() if key != "role"})
        flags_script = "import json,pathlib;print(json.dumps({p.name:int((p/'flags').read_text().strip(),16) for p in pathlib.Path('/sys/class/net').iterdir() if p.is_dir()}))"
        flags = json.loads(self.command(self.restore.docker + ["exec", self.mock_id, "python3", "-c", flags_script], timeout=10)[0])
        # Docker Desktop may expose DOWN kernel tunnel devices in network:none.
        # Refuse every UP interface except loopback before attempting probes.
        validate_interface_flags(flags)
        script = """import json,socket
assert len(open('/proc/net/route').read().splitlines())==1
for family,host in [(socket.AF_INET,'192.0.2.1'),(socket.AF_INET,'172.17.0.1'),(socket.AF_INET,'169.254.169.254'),(socket.AF_INET6,'2001:db8::1')]:
 s=socket.socket(family,socket.SOCK_STREAM);s.settimeout(.2)
 try:
  s.connect((host,443));raise AssertionError('external_route_available')
 except OSError: pass
 finally:s.close()
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
try:
 s.connect(('192.0.2.53',53));s.send(b'synthetic');raise AssertionError('dns_route_available')
except OSError: pass
finally:s.close()
print(json.dumps({'offline':True,'active_interfaces':['lo'],'external_connect_refusals':4,'dns_route_refused':True}))
"""
        result = json.loads(self.command(self.restore.docker + ["exec", self.mock_id, "python3", "-c", script], timeout=10)[0])
        self.report["network_probes"] = result
        return result

    def _remove_container(self, container):
        record = self.containers[container]
        validate_container(self.inspect(container), project=self.project, **{key: value for key, value in record.items() if key != "role"})
        diagnostic_error = None
        try:
            self.command(self.restore.docker + ["logs", "--tail", "1000", container], allowed=(0, 1))
        except BaseException as error:
            diagnostic_error = error
        finally:
            # Diagnostics must never prevent the removal of a verified owned
            # container, including capture overflow or an interruption.
            self.command(self.restore.docker + ["rm", "--force", container], timeout=30)
            del self.containers[container]
            self.journal("container_removed", container_id=container)
        if diagnostic_error:
            raise diagnostic_error

    def cleanup(self):
        failures = []
        previous = signal.getsignal(signal.SIGTERM)
        def interrupted(_signum, _frame):
            self._failed = True
            session = getattr(self, "session", None)
            if session is not None:
                session._failed = True
            if "interrupted" not in failures:
                failures.append("interrupted")
        # The outer restore handler raises in its callback phase. Application
        # resources must finish their teardown before control returns there.
        signal.signal(signal.SIGTERM, interrupted)
        try:
            self.restore._stage = "application_cleanup"
            try:
                self.restore._journal("stage")
            except BaseException as error:
                failures.append(type(error).__name__)
            self._cleanup_resources(failures)
            if failures:
                try:
                    self.journal("cleanup_unverified", failures=failures)
                except BaseException:
                    pass
                raise Refused("application_cleanup_unverified")
            self.journal("cleanup_verified")
        finally:
            signal.signal(signal.SIGTERM, previous)

    def _cleanup_resources(self, failures):
        for container in reversed(list(self.containers)):
            try:
                self._remove_container(container)
            except BaseException as error:
                failures.append(type(error).__name__)
        if self._volume_created:
            try:
                volume = json.loads(self.command(self.restore.docker + ["volume", "inspect", self.config_volume])[0])[0]
                if (volume.get("Name") != self.config_volume or volume.get("Driver") != "local" or volume.get("Options")
                        or volume.get("Labels", {}).get("com.docker.compose.project") != self.project):
                    raise Refused("application_cleanup_volume_ownership_mismatch")
                self.command(self.restore.docker + ["volume", "rm", self.config_volume], timeout=30)
                self._volume_created = False
            except BaseException as error:
                failures.append(type(error).__name__)
        for role, tag in reversed(list(getattr(self, "tags", {}).items())):
            try:
                raw, status = self.command(self.restore.docker + ["image", "inspect", tag], allowed=(0, 1))
                if status == 0:
                    info = json.loads(raw)[0]
                    expected = self.image_ids.get(role)
                    if (info.get("Config", {}).get("Labels", {}).get("gnomonitoring.synthetic.project") != self.project
                            or (expected is not None and info.get("Id") != expected)):
                        raise Refused("application_cleanup_image_ownership_mismatch")
                    self.command(self.restore.docker + ["image", "rm", tag])
            except BaseException as error:
                failures.append(type(error).__name__)
        for kind, selector in [("ps", ["--all", "--quiet"]), ("volume", ["ls", "--quiet"])]:
            try:
                remaining = self.command(self.restore.docker + [kind, *selector, "--filter", "label=com.docker.compose.project=" + self.project])[0]
                if remaining.strip():
                    failures.append("owned_resources_remain")
            except BaseException as error:
                failures.append(type(error).__name__)
        for selector in ("reference=" + self.project + "-*:local-only", "label=gnomonitoring.synthetic.project=" + self.project):
            try:
                remaining = self.command(self.restore.docker + ["image", "ls", "--all", "--quiet", "--filter", selector])[0]
                if remaining.strip():
                    failures.append("owned_images_remain")
            except BaseException as error:
                failures.append(type(error).__name__)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute-synthetic-application", action="store_true")
    parser.add_argument("--private-output-dir")
    args = parser.parse_args(argv)
    if not args.execute_synthetic_application:
        print(json.dumps({"status": "tools_only", "action": "none", "synthetic_only": True}))
        return 0
    if not args.private_output_dir:
        parser.error("--private-output-dir is required")
    os.umask(0o077)
    try:
        with ApplicationRunner(args.private_output_dir).open_application() as application:
            application.start_backend()
        print(json.dumps({"status": "synthetic_application_completed", "synthetic_only": True}))
        return 0
    except (KeyboardInterrupt, Exception):
        print(json.dumps({"status": "synthetic_application_failed", "synthetic_only": True}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
