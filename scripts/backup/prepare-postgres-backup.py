#!/usr/bin/env python3
"""Check an explicit PostgreSQL backup target; export only with --execute.

No restore, service operation, remote connection, or upload is implemented.
The operator must separately approve the destination before production export.
"""

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile
import threading


LOG_LIMIT = 64 * 1024
METADATA_LIMIT = 64 * 1024
GIB = 1024**3
IDENTIFIER = re.compile(r"[A-Za-z_][A-Za-z0-9_.-]{0,62}\Z")
CONTAINER = re.compile(r"[0-9a-f]{64}\Z")
IMAGE = re.compile(r"sha256:[0-9a-f]{64}\Z")
SOCKET_DIRECTORY = re.compile(r"/(?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+\Z")
INSPECT_FORMAT = (
    '{"id":{{json .Id}},"image":{{json .Image}},'
    '"status":{{json .State.Status}},"project":'
    '{{with index . "Config"}}{{with index . "Labels"}}'
    '{{with index . "com.docker.compose.project"}}{{json .}}'
    '{{else}}""{{end}}{{else}}""{{end}}{{else}}""{{end}}}'
)


class BackupError(Exception):
    pass


def utc_now():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def private_file(path):
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW
    return os.fdopen(os.open(path, flags, 0o600), "wb")


def local_environment():
    return {key: value for key, value in os.environ.items() if not key.startswith("DOCKER_")}


def validate_docker_socket(value):
    path = Path(value)
    if not path.is_absolute() or ".." in path.parts or any(ord(c) < 32 for c in value):
        raise BackupError("An explicit absolute local Docker Unix socket is required.")
    try:
        path = path.resolve(strict=True)
        info = path.stat()
    except OSError:
        raise BackupError("The approved local Docker socket does not exist.") from None
    if not stat.S_ISSOCK(info.st_mode) or info.st_uid not in (0, os.geteuid()) or info.st_mode & 0o002:
        raise BackupError("Docker target must be a user/root-owned local Unix socket without public write access.")
    return str(path)


def docker_command(args, command):
    return ["docker", "--host", "unix://" + args.docker_socket, *command]


def validate_destination(value):
    path = Path(value)
    if not path.is_absolute() or ".." in path.parts:
        raise BackupError("Destination must be an explicit absolute canonical directory.")
    try:
        resolved = path.resolve(strict=True)
        info = path.lstat()
    except OSError:
        raise BackupError("Destination must already exist.") from None
    if resolved != path or path.is_symlink() or not stat.S_ISDIR(info.st_mode):
        raise BackupError("Destination must be a directory without symlink components.")
    if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise BackupError("Destination must belong to this user and have mode 0700.")
    if not os.access(path, os.W_OK | os.X_OK):
        raise BackupError("Destination is not writable by this user.")
    for parent in (path, *path.parents):
        if (parent / ".git").exists():
            raise BackupError("Backup destination must be outside a Git checkout.")
        ancestor = parent.stat()
        if ancestor.st_uid not in (0, os.geteuid()):
            raise BackupError("Destination ancestors must belong to this user or root.")
        if ancestor.st_mode & 0o022 and not ancestor.st_mode & stat.S_ISVTX:
            raise BackupError("Destination has an untrusted writable ancestor.")
    return path


class Runner:
    """Drain stderr completely, retaining at most 64 KiB in a private file."""

    def __init__(self, directory):
        self.directory = Path(directory)
        self.counter = 0

    def run(self, command, *, timeout=30, output=None, input_file=None, reject_stderr=False):
        self.counter += 1
        log = self.directory / f"command-{self.counter:02d}.stderr"
        capture = output is None
        captured = bytearray()
        overflow = [False]
        stderr_bytes = [0]
        drain_errors = []
        interrupted = False

        def drain(stream, destination, limit, is_stderr=False):
            retained = 0
            while chunk := stream.read(8192):
                if is_stderr:
                    stderr_bytes[0] += len(chunk)
                keep = chunk[: max(0, limit - retained)]
                try:
                    if isinstance(destination, bytearray):
                        destination.extend(keep)
                    else:
                        destination.write(keep)
                except OSError:
                    drain_errors.append(True)
                retained += len(keep)
                if len(keep) < len(chunk):
                    overflow[0] = True

        try:
            with private_file(log) as error_log:
                proc = subprocess.Popen(
                    command,
                    stdin=input_file if input_file is not None else subprocess.DEVNULL,
                    stdout=subprocess.PIPE if capture else output,
                    stderr=subprocess.PIPE,
                    shell=False,
                    env=local_environment(),
                )
                readers = [threading.Thread(target=drain, args=(proc.stderr, error_log, LOG_LIMIT, True))]
                if capture:
                    readers.append(threading.Thread(target=drain, args=(proc.stdout, captured, METADATA_LIMIT)))
                for reader in readers:
                    reader.start()
                try:
                    code = proc.wait(timeout=timeout)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                    code = -1
                except KeyboardInterrupt:
                    proc.kill()
                    proc.wait()
                    code = -1
                    interrupted = True
                for reader in readers:
                    reader.join()
                proc.stderr.close()
                if capture:
                    proc.stdout.close()
                error_log.flush()
                os.fsync(error_log.fileno())
        except (OSError, subprocess.SubprocessError):
            raise BackupError("A required local command could not run; no command output is printed.") from None
        if interrupted:
            raise KeyboardInterrupt
        if code != 0 or drain_errors or overflow[0] or (reject_stderr and stderr_bytes[0]):
            raise BackupError("A backup command failed or produced unaccepted diagnostics; stderr remains private.")
        if output is not None:
            output.flush()
            os.fsync(output.fileno())
        return bytes(captured)


def docker_exec(args, command, *, timeout_seconds=30, interactive=False):
    # Only nonsecret connection guards are passed. Existing container-local
    # authentication remains inside the confirmed PostgreSQL container.
    result = docker_command(args, ["exec"])
    if interactive:
        result.append("-i")
    result.extend(
        ["--env", "PGCONNECT_TIMEOUT=5", "--env", "PGAPPNAME=gnomonitoring_backup",
         "--env", "PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=5000",
         args.container_id, "env", "-u", "PGHOSTADDR", "-u", "PGSERVICE", "-u", "PGSERVICEFILE",
         "timeout", "--signal=TERM", "--kill-after=5s", f"{timeout_seconds}s"]
    )
    return result + command


def parse_version(output, program):
    text = output.decode("utf-8", errors="strict")
    match = re.fullmatch(rf"{program} \(PostgreSQL\) ([0-9]+)(?:\.[0-9]+)+(?:[^\n]*)\n?", text)
    if not match:
        raise BackupError("Unexpected PostgreSQL tool version output.")
    return int(match.group(1)), text.strip()


def check(args, runner):
    destination = validate_destination(args.destination)
    args.docker_socket = validate_docker_socket(args.docker_socket)
    if not CONTAINER.fullmatch(args.container_id) or not IMAGE.fullmatch(args.expected_image_id):
        raise BackupError("A full confirmed container ID and image ID are required.")
    for value in (args.database, args.user, args.compose_project):
        if not IDENTIFIER.fullmatch(value):
            raise BackupError("Database, user and Compose project must be explicit simple identifiers.")
    socket = Path(args.socket_directory)
    if not SOCKET_DIRECTORY.fullmatch(args.socket_directory) or ".." in socket.parts or not 1 <= args.port <= 65535:
        raise BackupError("A confirmed container-local absolute socket directory and valid port are required.")
    if args.postgres_major < 10 or args.postgres_major > 99:
        raise BackupError("A supported explicit PostgreSQL major is required.")

    inspected = json.loads(runner.run(docker_command(args, ["inspect", "--type", "container", "--format", INSPECT_FORMAT, args.container_id])))
    if inspected != {
        "id": args.container_id, "image": args.expected_image_id,
        "status": "running", "project": args.compose_project,
    }:
        raise BackupError("Confirmed container identity, image, state or Compose project changed.")

    versions = {}
    for name in ("pg_dump", "pg_dumpall", "pg_restore"):
        major, version = parse_version(runner.run(docker_exec(args, [name, "--version"])), name)
        if major != args.postgres_major:
            raise BackupError("PostgreSQL tool major differs from the confirmed server major.")
        versions[name] = version

    query = (
        "SELECT json_build_object('database', current_database(), 'user', current_user, "
        "'server_version_num', current_setting('server_version_num')::integer, "
        "'database_bytes', pg_database_size(current_database()), "
        "'transaction_read_only', current_setting('transaction_read_only'))"
    )
    raw = runner.run(docker_exec(args, [
        "psql", "--no-psqlrc", "--no-password", "--tuples-only", "--no-align",
        "--host", args.socket_directory, "--port", str(args.port),
        "--set", "ON_ERROR_STOP=1", "--username", args.user,
        "--dbname", args.database, "--command", query,
    ]))
    source = json.loads(raw)
    if (
        source.get("database") != args.database or source.get("user") != args.user
        or type(source.get("server_version_num")) is not int
        or source["server_version_num"] // 10000 != args.postgres_major
        or type(source.get("database_bytes")) is not int or source["database_bytes"] <= 0
        or source.get("transaction_read_only") != "on"
    ):
        raise BackupError("Database identity, size, server major or read-only session guard was not confirmed.")
    # A conservative floor, not a promise of compressed archive size. The
    # operator also confirms storage capacity, encryption and retention policy.
    required = 2 * source["database_bytes"] + GIB
    free = shutil.disk_usage(destination).free
    if free < required:
        raise BackupError("Destination has less than twice the database size plus 1 GiB free.")
    return {
        "format_version": 1,
        "kind": "gnomonitoring-postgres-backup",
        "checked_at_utc": utc_now(),
        "destination": str(destination),
        "source": {
            "docker_socket": args.docker_socket,
            "container_id": args.container_id, "image_id": args.expected_image_id,
            "compose_project": args.compose_project, "database": args.database,
            "user": args.user, "postgres_major": args.postgres_major,
            "socket_directory": args.socket_directory, "port": args.port,
            "server_version_num": source["server_version_num"],
            "database_bytes": source["database_bytes"], "tools": versions,
        },
        "required_free_bytes": required,
        "observed_free_bytes": free,
        "restore_verified": False,
    }


def digest(path):
    sha = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(1024 * 1024):
            sha.update(chunk)
    return sha.hexdigest()


def fsync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def publish_complete_manifest(bundle, manifest):
    """Publish only a synced file; invalidate the marker on detected failure."""
    pending = bundle / ".manifest.pending"
    final = bundle / "manifest.json"
    try:
        with private_file(pending) as output:
            output.write((json.dumps(manifest, indent=2) + "\n").encode())
            output.flush()
            os.fsync(output.fileno())
        # Persist archive/digest entries before publishing completion. Sync the
        # new marker and the bundle's own parent entry after its atomic rename.
        fsync_directory(bundle)
        os.replace(pending, final)
        fsync_directory(bundle)
        fsync_directory(bundle.parent)
    except BaseException:
        # Includes interruptions immediately after rename. No reader should
        # consume a bundle from a failed invocation; do not leave a completion
        # marker behind when a subsequent durability check failed.
        for path in (final, pending):
            path.unlink(missing_ok=True)
        try:
            fsync_directory(bundle)
        except OSError:
            pass
        raise


def execute(args, checked):
    destination = validate_destination(args.destination)
    name = "postgres-backup-" + dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ-") + secrets.token_hex(4)
    bundle = destination / name
    bundle.mkdir(mode=0o700)
    runner = Runner(bundle)
    try:
        # Recheck identity and space immediately before creating an export.
        checked = check(args, runner)
        checked["started_at_utc"] = utc_now()
        timeout = args.dump_timeout_seconds
        with private_file(bundle / "database.dump") as output:
            runner.run(docker_exec(args, [
                "pg_dump", "--no-password", "--username", args.user,
                "--host", args.socket_directory, "--port", str(args.port),
                "--dbname", args.database, "--format=custom", "--lock-wait-timeout=30000",
            ], timeout_seconds=timeout), timeout=timeout + 15, output=output, reject_stderr=True)
        with (bundle / "database.dump").open("rb") as source:
            if source.read(5) != b"PGDMP":
                raise BackupError("Export is not a PostgreSQL custom-format archive.")
        with private_file(bundle / "globals.sql") as output:
            runner.run(docker_exec(args, [
                "pg_dumpall", "--no-password", "--username", args.user,
                "--host", args.socket_directory, "--port", str(args.port),
                "--database", args.database, "--globals-only", "--lock-wait-timeout=30000",
            ], timeout_seconds=timeout), timeout=timeout + 15, output=output, reject_stderr=True)
        if (bundle / "globals.sql").stat().st_size == 0:
            raise BackupError("Globals export is empty.")
        with (bundle / "database.dump").open("rb") as source, private_file(bundle / "toc.list") as output:
            runner.run(docker_exec(args, ["pg_restore", "--list"], timeout_seconds=300, interactive=True),
                       timeout=315, input_file=source, output=output, reject_stderr=True)
        entries = sum(bool(line.strip()) and not line.startswith(";") for line in (bundle / "toc.list").read_text().splitlines())
        if entries == 0:
            raise BackupError("Archive has no table-of-contents entries.")
        files = {}
        for basename in ("database.dump", "globals.sql", "toc.list"):
            path = bundle / basename
            files[basename] = {"sha256": digest(path), "bytes": path.stat().st_size}
        checked.update({"completed_at_utc": utc_now(), "complete": True, "files": files, "toc_entries": entries})
        with private_file(bundle / "SHA256SUMS") as output:
            output.write("".join(f'{meta["sha256"]}  {name}\n' for name, meta in files.items()).encode())
            output.flush()
            os.fsync(output.fileno())
        publish_complete_manifest(bundle, checked)
    except (BackupError, OSError, ValueError, UnicodeError):
        # A partial directory is retained privately for diagnosis. It never
        # gains a complete manifest and must not be used for a rehearsal.
        raise BackupError(f"Export incomplete; private partial directory retained at {bundle}.") from None
    return bundle


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container-id", required=True)
    parser.add_argument("--docker-socket", required=True)
    parser.add_argument("--expected-image-id", required=True)
    parser.add_argument("--compose-project", required=True)
    parser.add_argument("--database", required=True)
    parser.add_argument("--user", required=True)
    parser.add_argument("--postgres-major", required=True, type=int)
    parser.add_argument("--socket-directory", required=True)
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--destination", required=True)
    parser.add_argument("--dump-timeout-seconds", type=int, default=1800)
    parser.add_argument("--execute", action="store_true", help="Export only after the operator separately approves this exact storage destination.")
    args = parser.parse_args(argv)
    if not 30 <= args.dump_timeout_seconds <= 86400:
        parser.error("dump timeout must be between 30 and 86400 seconds")
    os.umask(0o077)
    try:
        with tempfile.TemporaryDirectory(prefix="gnomonitoring-backup-check-") as tmp:
            checked = check(args, Runner(tmp))
        if args.execute:
            bundle = execute(args, checked)
            print(f"Private export complete (restore not verified): {bundle}")
        else:
            print("Backup checks passed; no export performed. Restore not verified.")
    except KeyboardInterrupt:
        print("Backup interrupted; no completed backup is claimed.", file=sys.stderr)
        return 130
    except BackupError as error:
        print(str(error), file=sys.stderr)
        return 1
    except (OSError, ValueError, UnicodeError):
        print("Backup preparation failed. No raw command output or credentials are printed; no completed backup is claimed.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
