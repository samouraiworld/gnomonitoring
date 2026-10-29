#!/usr/bin/env python3
"""Restore an explicit private backup into new, disposable, offline volumes.

No Docker command is performed without --execute-restore. Archives are executable
SQL, so hashes are integrity checks, not an authorization or trust decision.
"""

import argparse
from contextlib import contextmanager
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import signal
import stat
import subprocess
import sys
import threading

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
SPEC = importlib.util.spec_from_file_location("synthetic_fences", HERE.parent / "run-synthetic.py")
FENCES = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FENCES)
Refused = FENCES.Refused
COMPOSE = HERE / "compose.restore.yml"
GIB = 1024**3
LOG_LIMIT = 1024**2
METADATA_LIMIT = 4 * 1024**2
FILES = {"database.dump", "globals.sql", "toc.list"}
SOCKET = "/run/rehearsal-postgres"


def no_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise Refused("duplicate_json_key")
        result[key] = value
    return result


def private_directory(value, empty=False):
    path = Path(value)
    if not path.is_absolute() or ".." in path.parts or path.resolve(strict=True) != path:
        raise Refused("canonical_private_directory_required")
    info = path.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise Refused("private_owned_0700_directory_required")
    for parent in (path, *path.parents):
        if (parent / ".git").exists():
            raise Refused("private_directory_must_be_outside_git")
        info = parent.stat()
        if info.st_uid not in {0, os.getuid()} or (info.st_mode & 0o022 and not info.st_mode & stat.S_ISVTX):
            raise Refused("private_directory_has_untrusted_ancestor")
    if empty and any(path.iterdir()):
        raise Refused("empty_output_directory_required")
    return path


def open_private(path):
    # O_NONBLOCK lets fstat reject FIFOs/devices without waiting for a writer.
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    source = os.fdopen(descriptor, "rb")
    info = os.fstat(descriptor)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) not in {0o400, 0o600}:
        source.close()
        raise Refused("private_regular_owned_file_required")
    return source


def fingerprint(source):
    checksum = hashlib.sha256()
    source.seek(0)
    while chunk := source.read(1024**2):
        checksum.update(chunk)
    source.seek(0)
    return checksum.hexdigest()


def read_small(path, limit):
    with open_private(path) as source:
        if os.fstat(source.fileno()).st_size > limit:
            raise Refused("private_metadata_too_large")
        return source.read()


def validate_bundle(value):
    bundle = private_directory(value)
    manifest = json.loads(read_small(bundle / "manifest.json", 64 * 1024), object_pairs_hook=no_duplicate_keys)
    if (not isinstance(manifest, dict) or type(manifest.get("format_version")) is not int or manifest.get("format_version") != 1
            or manifest.get("kind") != "gnomonitoring-postgres-backup" or manifest.get("complete") is not True
            or manifest.get("restore_verified") is not False or set(manifest.get("files", {})) != FILES):
        raise Refused("unsupported_or_incomplete_bundle")
    source = manifest.get("source", {})
    if (not isinstance(source, dict) or type(source.get("postgres_major")) is not int
            or source.get("postgres_major") != 16 or type(source.get("database_bytes")) is not int
            or source["database_bytes"] <= 0):
        raise Refused("postgres16_source_size_required")
    for name in sorted(FILES):
        expected = manifest["files"][name]
        if (not isinstance(expected, dict) or type(expected.get("bytes")) is not int or expected["bytes"] <= 0
                or not isinstance(expected.get("sha256"), str) or not re.fullmatch("[0-9a-f]{64}", expected["sha256"])):
            raise Refused("invalid_bundle_file_metadata")
        with open_private(bundle / name) as handle:
            if os.fstat(handle.fileno()).st_size != expected["bytes"] or fingerprint(handle) != expected["sha256"]:
                raise Refused("bundle_size_or_hash_mismatch")
            if name == "database.dump" and handle.read(5) != b"PGDMP":
                raise Refused("custom_archive_required")
    sums = read_small(bundle / "SHA256SUMS", 4096).decode("ascii")
    parsed = {}
    for line in sums.splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  (database\.dump|globals\.sql|toc\.list)", line)
        if not match or match[2] in parsed:
            raise Refused("invalid_checksum_list")
        parsed[match[2]] = match[1]
    if parsed != {name: manifest["files"][name]["sha256"] for name in FILES}:
        raise Refused("checksum_list_mismatch")
    toc = read_small(bundle / "toc.list", METADATA_LIMIT).decode("utf-8")
    if not any(line and not line.startswith(";") for line in toc.splitlines()):
        raise Refused("empty_archive_toc")
    if re.search(r"(^|[^A-Za-z0-9_])rehearsal_guard([^A-Za-z0-9_]|$)", toc):
        raise Refused("reserved_guard_schema_in_archive")
    return manifest


def private_write(path, content):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as target:
        target.write(content)
        target.flush()
        os.fsync(target.fileno())


def quote_identifier(value):
    return '"' + value.replace('"', '""') + '"'


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def publish_summary(output, report):
    pending, final = output / ".restore-summary.pending", output / "restore-summary.json"
    try:
        private_write(pending, (json.dumps(report, indent=2) + "\n").encode())
        sync_directory(output)
        os.replace(pending, final)
        sync_directory(output)
    except BaseException:
        pending.unlink(missing_ok=True)
        final.unlink(missing_ok=True)
        raise


def validate_actual_container(info, project, service):
    FENCES.validate_container_fence(info, project)
    wanted = {(f"{project}-socket", SOCKET, service == "postgres"),
              (f"{project}-private-logs", "/run/rehearsal-private", True)}
    if service == "postgres":
        wanted.add((f"{project}-pgdata", "/var/lib/postgresql/data", True))
    actual = {(mount.get("Name"), mount.get("Destination"), mount.get("RW")) for mount in info.get("mounts", []) if mount.get("Type") == "volume"}
    if actual != wanted or len(info.get("mounts", [])) != len(wanted) or info.get("project") != project:
        raise Refused("actual_exact_owned_mounts_required")
    if service == "initdb" and not info.get("read_only"):
        raise Refused("actual_read_only_initdb_root_required")


class RestoreRunner(FENCES.Runner):
    def __init__(self, bundle, output, docker_socket=None, timeout_seconds=1800):
        self.bundle = private_directory(bundle)
        self.manifest = validate_bundle(self.bundle)
        output = private_directory(output, empty=True)
        if output == self.bundle or output in self.bundle.parents or self.bundle in output.parents:
            raise Refused("bundle_and_output_must_be_separate")
        if not 30 <= timeout_seconds <= 86400:
            raise Refused("timeout_out_of_bounds")
        self.timeout_seconds = timeout_seconds
        super().__init__(output, docker_socket)
        self.project = "gnomon-restore-" + self.clone_id
        self.database = "gnomonitoring_rehearsal_" + self.clone_id
        self.environment["REHEARSAL_PROJECT"] = self.project
        self.environment["RESTORE_ADMIN_PASSWORD"] = self.environment.pop("REHEARSAL_SYNTHETIC_ADMIN_PASSWORD")
        self.compose = self.docker + ["compose", "--project-name", self.project, "--file", str(COMPOSE), "--project-directory", str(HERE)]
        self.postgres_id = None
        self.created = False
        self.stage = "preflight"

    def command(self, arguments, allowed=(0,), timeout=600, input_file=None, reject_stderr=False):
        if arguments[0] == "docker" and arguments[:3] != self.docker:
            raise Refused("unpinned_docker_endpoint_refused")
        self.sequence += 1
        streams = {}
        capture = bytearray()
        overflow = [False]
        diagnostic = [False]
        failure = [False]
        def drain(source, destination, keep_capture=False):
            retained = 0
            try:
                while chunk := source.read(8192):
                    if not keep_capture:
                        diagnostic[0] = True
                    limit = METADATA_LIMIT if keep_capture else LOG_LIMIT
                    kept = chunk[:max(0, limit-retained)]
                    destination.write(kept)
                    if keep_capture:
                        capture.extend(kept)
                    retained += len(kept)
                    if len(kept) < len(chunk):
                        overflow[0] = True
            except OSError:
                failure[0] = True
        try:
            for name in ("stdout", "stderr"):
                path = self.output / f"{self.sequence:03d}-{name}.private"
                streams[name] = os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb")
            process = subprocess.Popen(arguments, cwd=ROOT, env=self.environment, stdin=input_file or subprocess.DEVNULL,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            readers = [threading.Thread(target=drain, args=(process.stdout, streams["stdout"], True)),
                       threading.Thread(target=drain, args=(process.stderr, streams["stderr"]))]
            for reader in readers:
                reader.start()
            try:
                status = process.wait(timeout=timeout)
            except BaseException:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                raise
            finally:
                for reader in readers:
                    reader.join()
                process.stdout.close()
                process.stderr.close()
        finally:
            for stream in streams.values():
                stream.flush()
                os.fsync(stream.fileno())
                stream.close()
        if status not in allowed or overflow[0] or failure[0] or (reject_stderr and diagnostic[0]):
            raise Refused(f"restore_step_failed_{self.stage}_exit_{status}")
        return capture.decode("utf-8"), status

    def exec_pg(self, arguments, *, role="rehearsal_owner", timeout=30, input_file=None, reject_stderr=True):
        command = self.docker + ["exec", "-i", "--user", "999:999", self.postgres_id, "env",
            "-u", "PGHOSTADDR", "-u", "PGSERVICE", "-u", "PGSERVICEFILE", "-u", "PGPASSWORD", "-u", "PGOPTIONS",
            "PGPASSFILE=" + SOCKET + "/empty.pgpass", "PGCONNECT_TIMEOUT=5", "PGAPPNAME=gnomonitoring_isolated_restore",
            f"PGOPTIONS=-c statement_timeout={timeout * 1000} -c lock_timeout=5000 -c search_path=pg_catalog -c standard_conforming_strings=on",
            "timeout", "--signal=TERM", "--kill-after=5s", str(timeout)+"s"]
        return self.command(command + arguments, timeout=timeout+15, input_file=input_file, reject_stderr=reject_stderr)[0]

    def sql(self, sql, role="rehearsal_owner", database=None, timeout=30):
        return self.exec_pg(["psql", "-X", "-q", "-t", "-A", "-w", "-h", SOCKET, "-U", role,
                            "-d", database or self.database, "-v", "ON_ERROR_STOP=1", "-c", sql], role=role, timeout=timeout)

    def inspect_container(self, container, service):
        template = '{"network_mode":{{json .HostConfig.NetworkMode}},"ports":{{json .NetworkSettings.Ports}},"privileged":{{json .HostConfig.Privileged}},"pid_mode":{{json .HostConfig.PidMode}},"user":{{json .Config.User}},"cap_add":{{json .HostConfig.CapAdd}},"cap_drop":{{json .HostConfig.CapDrop}},"security_opt":{{json .HostConfig.SecurityOpt}},"read_only":{{json .HostConfig.ReadonlyRootfs}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"mounts":{{json .Mounts}}}'
        raw, _ = self.command(self.docker + ["inspect", "--format", template, container])
        validate_actual_container(json.loads(raw), self.project, service)

    def disk_free(self):
        output = self.exec_pg(["df", "-B1", "--output=avail", "/var/lib/postgresql/data"])
        lines = output.strip().splitlines()
        if len(lines) != 2 or not lines[1].strip().isdigit():
            raise Refused("docker_volume_capacity_unavailable")
        return int(lines[1].strip())

    def baseline(self):
        relations = json.loads(self.sql("""SELECT coalesce(json_agg(json_build_object('schema',n.nspname,'name',c.relname,'kind',c.relkind,'owner',pg_get_userbyid(c.relowner))), '[]')
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE c.relkind IN ('r','p','v','m','S') AND n.nspname !~ '^pg_' AND n.nspname NOT IN ('information_schema','rehearsal_guard')"""))
        if len(relations) > 500:
            raise Refused("catalogue_relation_budget_exceeded")
        result = {"tables": [], "views": [], "sequences": []}
        for relation in sorted(relations, key=lambda item: (item["schema"], item["name"])):
            schema, name = relation["schema"], relation["name"]
            qualified = quote_identifier(schema) + "." + quote_identifier(name)
            if relation["kind"] == "S":
                value = json.loads(self.sql(f"SELECT row_to_json(x) FROM (SELECT last_value,is_called FROM {qualified}) x"))
                result["sequences"].append({"schema": schema, "name": name, **value})
            elif relation["kind"] in {"v", "m"}:
                result["views"].append(relation)
            else:
                literal = qualified.replace("'", "''")
                info = json.loads(self.sql(f"""SELECT json_build_object(
'columns',(SELECT coalesce(json_agg(json_build_object('name',attname,'type',format_type(atttypid,atttypmod),'not_null',attnotnull) ORDER BY attnum),'[]') FROM pg_attribute WHERE attrelid='{literal}'::regclass AND attnum>0 AND NOT attisdropped),
'constraints',(SELECT coalesce(json_agg(json_build_object('name',conname,'type',contype,'validated',convalidated)),'[]') FROM pg_constraint WHERE conrelid='{literal}'::regclass),
'indexes',(SELECT coalesce(json_agg(json_build_object('name',c.relname,'valid',i.indisvalid)),'[]') FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE i.indrelid='{literal}'::regclass))"""))
                chain = any(column["name"] == "chain_id" for column in info["columns"])
                if chain:
                    scopes = json.loads(self.sql(f"SELECT coalesce(json_agg(x),'[]') FROM (SELECT CASE WHEN chain_id IS NULL THEN 'null_chain_id' ELSE 'chain' END AS scope, chain_id, count(*) AS rows FROM {qualified} GROUP BY chain_id ORDER BY chain_id NULLS FIRST) x"))
                else:
                    count = int(self.sql(f"SELECT count(*) FROM {qualified}").strip())
                    scopes = [{"scope": "global", "rows": count}]
                result["tables"].append({"schema": schema, "name": name, "owner": relation["owner"], "rows": sum(scope["rows"] for scope in scopes),
                                         "chain_column_present": chain, "scopes": scopes, **info})
        result["extensions"] = json.loads(self.sql("SELECT coalesce(json_agg(json_build_object('name',extname,'version',extversion)),'[]') FROM pg_extension"))
        result["roles"] = json.loads(self.sql("SELECT coalesce(json_agg(rolname),'[]') FROM pg_roles"))
        return result

    def seal_marker(self):
        if self.sql("SELECT to_regnamespace('rehearsal_guard') IS NULL").strip() != "t":
            raise Refused("reserved_guard_schema_already_present")
        self.sql(f"""CREATE SCHEMA rehearsal_guard AUTHORIZATION rehearsal_admin;
CREATE TABLE rehearsal_guard.clone_marker (clone_id text PRIMARY KEY,database_name text NOT NULL,base_revision text NOT NULL);
INSERT INTO rehearsal_guard.clone_marker VALUES ('{self.clone_id}','{self.database}','{FENCES.BASE}');
REVOKE ALL ON SCHEMA rehearsal_guard FROM PUBLIC;
REVOKE ALL ON rehearsal_guard.clone_marker FROM PUBLIC;
GRANT USAGE ON SCHEMA rehearsal_guard TO rehearsal_owner;
GRANT SELECT ON rehearsal_guard.clone_marker TO rehearsal_owner;""", role="rehearsal_admin")
        dsn = f"postgresql://rehearsal_owner@/{self.database}?host={SOCKET}&sslmode=disable\n"
        # Docker stdin needs a real descriptor, so use a private local file and
        # remove it immediately after the constant, password-free DSN is sent.
        path = self.output / "clone-dsn.pending.private"
        private_write(path, dsn.encode())
        try:
            with path.open("rb") as source:
                self.exec_pg(["sh", "-c", "umask 077; cat > /run/rehearsal-postgres/clone.dsn; chmod 0600 /run/rehearsal-postgres/clone.dsn"],
                             input_file=source)
        finally:
            path.unlink(missing_ok=True)
        mode = self.exec_pg(["stat", "-c", "%a", SOCKET + "/clone.dsn"]).strip()
        state = json.loads(self.sql("""SELECT json_build_object(
'schema_owner',(SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='rehearsal_guard'),
'table_owner',(SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='rehearsal_guard.clone_marker'::regclass),
'app_can_write',has_table_privilege('rehearsal_owner','rehearsal_guard.clone_marker','INSERT,UPDATE,DELETE,TRUNCATE'),
'app_can_create',has_schema_privilege('rehearsal_owner','rehearsal_guard','CREATE'))"""))
        if mode != "600" or state != {"schema_owner": "rehearsal_admin", "table_owner": "rehearsal_admin", "app_can_write": False, "app_can_create": False}:
            raise Refused("independent_marker_not_sealed")
        return {**state, "dsn_mode": "0600"}

    def _prepare_clone(self, run_initdb=False):
        self.manifest = validate_bundle(self.bundle)
        helper_sources = self.pin_sources() if run_initdb else None
        if self.command(self.docker + ["info", "--format", "{{.OSType}}"])[0].strip() != "linux":
            raise Refused("linux_local_engine_required")
        self.fresh_volumes()
        config = json.loads(self.command(self.compose + ["config", "--format", "json"])[0])
        FENCES.validate_config(config, self.project)
        frozen = self.output / "resolved-compose.private.json"
        private_write(frozen, (json.dumps(config) + "\n").encode())
        self.compose = self.docker + ["compose", "--project-name", self.project, "--file", str(frozen), "--project-directory", str(HERE)]
        self.created = True
        self._journal("resources_planned")
        self.stage = "build_empty_clone"
        services = ["postgres", "initdb"] if run_initdb else ["postgres"]
        self.command(self.compose + ["build", *services], timeout=1200)
        self.stage = "create_before_start"
        self.command(self.compose + ["create", "postgres"])
        self.inspect_volume_fence()
        self.postgres_id = self.command(self.compose + ["ps", "--all", "--quiet", "postgres"])[0].strip()
        self._journal("container_identified", container_id=self.postgres_id, service="postgres")
        self.inspect_container(self.postgres_id, "postgres")
        self.command(self.docker + ["start", self.postgres_id])
        self.wait_postgres(self.postgres_id)
        if self.sql("SELECT current_setting('server_version_num')").strip() != "160015":
            raise Refused("pinned_postgres_version_changed")
        free = self.disk_free()
        required = 2 * self.manifest["source"]["database_bytes"] + GIB
        if free < required:
            raise Refused("docker_volume_free_space_insufficient")
        self.stage = "verify_actual_archive_toc"
        with open_private(self.bundle / "database.dump") as archive:
            expected = self.manifest["files"]["database.dump"]
            if os.fstat(archive.fileno()).st_size != expected["bytes"] or fingerprint(archive) != expected["sha256"]:
                raise Refused("archive_changed_before_restore")
            identity = os.fstat(archive.fileno())
            actual_toc = self.exec_pg(["pg_restore", "--list"], input_file=archive)
            stored_toc = read_small(self.bundle / "toc.list", METADATA_LIMIT).decode("utf-8")
            if actual_toc.strip() != stored_toc.strip():
                raise Refused("actual_archive_toc_mismatch")
            archive.seek(0)
            self.stage = "restore_nonprivileged_transaction"
            self.exec_pg(["pg_restore", "--no-owner", "--no-privileges", "--no-tablespaces", "--exit-on-error", "--single-transaction",
                          "--no-password", "-h", SOCKET, "-U", "rehearsal_owner", "-d", self.database],
                         timeout=self.timeout_seconds, input_file=archive)
            now = os.fstat(archive.fileno())
            if (identity.st_ino, identity.st_dev, identity.st_size, identity.st_mtime_ns) != (now.st_ino, now.st_dev, now.st_size, now.st_mtime_ns):
                raise Refused("archive_changed_during_restore")
        self.stage = "validate_restored_catalogue"
        restored = self.baseline()
        self.stage = "analyze_restored_tables"
        for table in restored["tables"]:
            self.sql("ANALYZE " + quote_identifier(table["schema"]) + "." + quote_identifier(table["name"]), timeout=self.timeout_seconds)
        if self.disk_free() < GIB:
            raise Refused("docker_volume_post_restore_margin_insufficient")
        self.stage = "seal_validated_clone"
        marker = self.seal_marker()
        self._report = {"format_version": 1, "kind": "gnomonitoring-isolated-restore", "restore_verified": True,
                  "source_snapshot_equality_verified": False, "globals_applied": False, "network_mode": "none",
                  "clone_id": self.clone_id, "project": self.project, "baseline": restored, "marker_sealed": True,
                  "marker_state": marker, "initdb": None, "initdb_provenance": helper_sources,
                  "source_archive_sha256": expected["sha256"],
                  "pre_restore_docker_volume_free_bytes": free, "required_free_bytes": required}
        if run_initdb:
            self._run_initdb()

    def _run_initdb(self):
        if self._report["initdb"] is not None:
            raise Refused("initdb_already_run_in_session")
        if self._report["initdb_provenance"] is None:
            self._report["initdb_provenance"] = self.pin_sources()
            self.stage = "build_initdb_for_session"
            self.command(self.compose + ["build", "initdb"], timeout=1200)
        self.stage = "initdb_after_verified_restore"
        self.command(self.compose + ["create", "initdb"])
        container = self.command(self.compose + ["ps", "--all", "--quiet", "initdb"])[0].strip()
        self._journal("container_identified", container_id=container, service="initdb")
        self.inspect_container(container, "initdb")
        first, status1 = self.start_initdb(container)
        second, status2 = self.start_initdb(container)
        if status1 not in {0, 3} or status2 not in {0, 3}:
            raise Refused("initdb_failed_after_restore")
        self._report["initdb"] = {"first_pass": first, "second_pass": second,
                                  "review_required": status1 == 3 or status2 == 3}
        return copy.deepcopy(self._report["initdb"])

    @property
    def stage(self):
        return self._stage

    @stage.setter
    def stage(self, value):
        self._stage = value
        if getattr(self, "_journal_ready", False):
            self._journal("stage")

    def _journal(self, event, **details):
        record = {"event": event, "stage": self.stage, "project": self.project,
                  "clone_id": self.clone_id,
                  "volumes": [self.project + suffix for suffix in ("-pgdata", "-socket", "-private-logs")],
                  **details}
        path = self.output / "session-journal.private.jsonl"
        descriptor = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        with os.fdopen(descriptor, "ab") as target:
            info = os.fstat(target.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
                raise Refused("private_session_journal_required")
            target.write((json.dumps(record, sort_keys=True) + "\n").encode())
            target.flush()
            os.fsync(target.fileno())
        sync_directory(self.output)

    @contextmanager
    def open_clone(self, run_initdb=False):
        """Own one validated offline clone until context exit; never resume it.

        Only a successful body and verified cleanup permit atomic publication.
        SIGTERM becomes KeyboardInterrupt for this main-thread-only lifecycle.
        """
        if threading.current_thread() is not threading.main_thread():
            raise Refused("clone_session_requires_main_thread")
        if getattr(self, "_session_used", False):
            raise Refused("clone_session_is_single_use")
        self._session_used = True
        self.created = False
        self.postgres_id = None
        self._journal_ready = True
        self._report = None
        session = CloneSession(self)
        previous = signal.getsignal(signal.SIGTERM)
        cleaning_up = False
        def interrupted(_signum, _frame):
            session._failed = True
            if not cleaning_up:
                raise KeyboardInterrupt
        signal.signal(signal.SIGTERM, interrupted)
        error = None
        try:
            self._journal("session_opening")
            self._prepare_clone(run_initdb)
            self.stage = "validated_clone_session"
            session._active = True
            yield session
            if session._failed:
                raise Refused("clone_session_operation_failed")
        except BaseException as caught:
            error = caught
            try:
                self._journal("session_failed", error_type=type(caught).__name__)
            except BaseException:
                pass  # Preserve the initial cause; cleanup must still be attempted.
            raise
        finally:
            session._active = False
            # SIGTERM still poisons completion during cleanup, but must not
            # interrupt the mandatory removal/verification attempt.
            cleaning_up = True
            try:
                if self.created:
                    self._stage = "cleanup_owned_resources"
                    try:
                        self._journal("stage")
                    except BaseException:
                        pass  # Journal I/O must never prevent the cleanup attempt.
                    self.cleanup(self.postgres_id)
                    self._journal("cleanup_verified")
            except BaseException:
                try:
                    self._journal("cleanup_unverified")
                except BaseException:
                    pass
                if error is None:
                    raise
                print(json.dumps({"status": "restore_cleanup_failed"}))
            finally:
                signal.signal(signal.SIGTERM, previous)
        if session._failed:
            raise Refused("clone_session_operation_failed")
        self._report["owned_resources_cleanup_verified"] = True
        self.stage = "publish_after_verified_cleanup"
        publish_summary(self.output, self._report)

    def execute(self, run_initdb=False):
        with self.open_clone(run_initdb) as session:
            report = session.report
        report["owned_resources_cleanup_verified"] = True
        return report


class CloneSession:
    """Bounded operations on a runner-owned target, valid only inside its context."""

    def __init__(self, runner):
        self._runner = runner
        self._active = False
        self._failed = False

    def _check_active(self):
        if not self._active:
            raise Refused("clone_session_is_closed")
        if self._failed:
            raise Refused("clone_session_operation_failed")

    @property
    def baseline(self):
        self._check_active()
        return copy.deepcopy(self._runner._report["baseline"])

    @property
    def report(self):
        self._check_active()
        return copy.deepcopy(self._runner._report)

    def sql(self, query, timeout=30):
        self._check_active()
        try:
            if not isinstance(query, str) or type(timeout) is not int or not 1 <= timeout <= self._runner.timeout_seconds:
                raise Refused("session_sql_budget_invalid")
            self._runner.stage = "session_sql"
            # A fixed SQL prefix prevents psql -c from dispatching a caller's
            # backslash command (notably \connect) as a client-side operation.
            return self._runner.sql("/* controlled clone session */\n" + query, timeout=timeout)
        except BaseException:
            self._failed = True
            raise

    def run_initdb(self):
        self._check_active()
        try:
            return self._runner._run_initdb()
        except BaseException:
            self._failed = True
            raise


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute-restore", action="store_true")
    parser.add_argument("--bundle")
    parser.add_argument("--private-output-dir")
    parser.add_argument("--docker-socket")
    parser.add_argument("--timeout-seconds", type=int, default=1800)
    parser.add_argument("--run-initdb", action="store_true")
    args = parser.parse_args(argv)
    if not args.execute_restore:
        print(json.dumps({"status": "tools_only", "action": "none"}))
        return 0
    os.umask(0o077)
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    try:
        if not args.bundle or not args.private_output_dir:
            raise Refused("explicit_bundle_and_private_output_required")
        report = RestoreRunner(args.bundle, args.private_output_dir, args.docker_socket, args.timeout_seconds).execute(args.run_initdb)
        review = bool(report["initdb"] and report["initdb"]["review_required"])
        print(json.dumps({"status": "restored_requires_initdb_review" if review else "restore_verified",
                          "globals_applied": False, "owned_resources_cleanup_verified": True}))
        return 3 if review else 0
    except KeyboardInterrupt:
        print(json.dumps({"status": "restore_interrupted", "restore_verified": False}))
        return 130
    except Refused as error:
        print(json.dumps({"status": "restore_refused_or_failed", "restore_verified": False, "error_class": str(error)}))
        return 1
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError):
        # Diagnostics and potentially sensitive SQL remain in the private output.
        print(json.dumps({"status": "restore_refused_or_failed", "restore_verified": False}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
