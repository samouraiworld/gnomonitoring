#!/usr/bin/env python3
"""Plan, or explicitly apply, one digest-pinned backend Compose replacement.

No registry access, environment/config dump, database operation or notification.
Real-session and real-recovery acceptance are operator prerequisites, not claims
made by this tool. A successful apply still requires functional acceptance.
"""
import argparse
from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys


IMAGE = re.compile(r"ghcr\.io/samouraiworld/gnomonitoring-server@sha256:[0-9a-f]{64}\Z")
IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}\Z")
CONTAINER_ID = re.compile(r"[0-9a-f]{64}\Z")
REVISION = re.compile(r"[0-9a-f]{40}\Z")
SOURCE = "https://github.com/samouraiworld/gnomonitoring"
SERVICE = "detect-proposal"
# Read only selected metadata. Never request full inspect output or Env.
CONTAINER_FORMAT = '''{"id":{{json .Id}},"image":{{json .Image}},
"status":{{json .State.Status}},"started":{{json .State.StartedAt}},
"health":{{with .State.Health}}{{json .Status}}{{else}}null{{end}},
"project":{{json (index .Config.Labels "com.docker.compose.project")}},
"service":{{json (index .Config.Labels "com.docker.compose.service")}},
"files":{{json (index .Config.Labels "com.docker.compose.project.config_files")}},
"directory":{{json (index .Config.Labels "com.docker.compose.project.working_dir")}}}'''
IMAGE_FORMAT = '''{"id":{{json .Id}},"digests":{{json .RepoDigests}},
"revision":{{json (index .Config.Labels "org.opencontainers.image.revision")}},
"source":{{json (index .Config.Labels "org.opencontainers.image.source")}}}'''


class Refused(Exception):
    """Diagnostic contains only a fixed class, never Docker error output."""


def require(condition, diagnostic):
    if not condition:
        raise Refused(diagnostic)


def docker(*args):
    try:
        result = subprocess.run(["docker", *args], capture_output=True, text=True,
                                check=False, timeout=180)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise Refused("docker_unavailable_or_timeout") from exc
    require(result.returncode == 0, "docker_command_failed")
    return result.stdout


def inspect_container(container):
    require(bool(CONTAINER_ID.fullmatch(container)), "container_id_invalid")
    return json.loads(docker("inspect", "--format", CONTAINER_FORMAT, container))


def inspect_image(image):
    return json.loads(docker("image", "inspect", "--format", IMAGE_FORMAT, image))


def snapshot(compose):
    ids = docker(*compose, "ps", "--all", "--quiet").split()
    require(bool(ids) and len(ids) == len(set(ids)), "project_empty_or_duplicate_ids")
    return {container: inspect_container(container) for container in ids}


def write_new(path, value):
    # No symlink following or replacement of an existing release/receipt file.
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        stream.write(value)
        stream.flush()
        os.fsync(stream.fileno())


@contextmanager
def rollout_lock(directory):
    descriptor = os.open(directory / ".backend-rollout.lock",
                         os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(descriptor)


def prepare(args):
    require(bool(IMAGE.fullmatch(args.image)), "digest_reference_required")
    require(bool(REVISION.fullmatch(args.revision)), "full_revision_required")
    require(bool(IMAGE_ID.fullmatch(args.expected_backend_image)), "previous_image_id_required")
    require(bool(CONTAINER_ID.fullmatch(args.backend_container)), "backend_id_invalid")
    require(bool(re.fullmatch(r"[a-z0-9][a-z0-9_-]*", args.project)), "project_name_invalid")
    directory = args.project_directory.resolve(strict=True)
    require(directory.is_dir(), "project_directory_invalid")
    files = []
    for name in args.compose_file:
        path = Path(name)
        if not path.is_absolute():
            path = directory / path
        path = path.resolve(strict=True)
        require(path.is_file() and "," not in str(path), "compose_file_invalid")
        files.append(str(path))
    require(len(set(files)) == len(files), "compose_file_duplicate")
    # A new basename keeps the pin in the project, without overwriting live files.
    require(args.override == Path(args.override).name and
            bool(re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*\.ya?ml", args.override)),
            "override_basename_required")
    override = directory / args.override
    require(not override.exists() and not override.is_symlink(), "override_already_exists")
    compose = ["compose", "--project-name", args.project,
               "--project-directory", str(directory)]
    for path in files:
        compose.extend(["-f", path])
    before = snapshot(compose)
    require(args.backend_container in before, "backend_not_in_selected_project")
    backend = before[args.backend_container]
    require(backend["service"] == SERVICE and backend["status"] == "running" and
            backend["image"] == args.expected_backend_image, "backend_identity_or_state_changed")
    require(sum(row["service"] == SERVICE for row in before.values()) == 1,
            "backend_replica_count_unsupported")
    require(len(before) > 1, "no_protected_services")
    for row in before.values():
        require(row["project"] == args.project and row["directory"] == str(directory) and
                row["files"] == ",".join(files), "effective_compose_mismatch")
        require(row["status"] == "running" and row["health"] not in ("unhealthy", "starting"),
                "existing_service_not_ready")
    candidate = inspect_image(args.image)
    require(args.image in (candidate["digests"] or []) and
            bool(IMAGE_ID.fullmatch(candidate["id"])) and
            candidate["revision"] == args.revision and candidate["source"] == SOURCE,
            "candidate_provenance_mismatch")
    previous = inspect_image(args.expected_backend_image)
    old_digests = [digest for digest in (previous["digests"] or []) if IMAGE.fullmatch(digest)]
    require(previous["id"] == args.expected_backend_image and bool(old_digests),
            "previous_digest_unavailable")
    command = compose + ["-f", str(override), "up", "-d", "--no-deps", "--no-build",
                         "--pull", "never", SERVICE]
    return directory, override, compose, before, candidate, previous, command


def run(args):
    prepared = prepare(args)
    directory, override, compose, before, candidate, previous, command = prepared
    record = {"format_version": 1, "status": "plan_only", "applied": False,
              "before": before, "candidate": candidate, "previous": previous,
              "command": ["docker", *command], "functional_acceptance": "pending"}
    if not args.apply:
        print(json.dumps(record, sort_keys=True))
        return
    require(args.receipt is not None, "receipt_path_required")
    receipt = args.receipt.absolute()
    require(not receipt.exists() and not receipt.is_symlink(), "receipt_already_exists")
    completion = Path(str(receipt) + ".applied.json")
    require(not completion.exists() and not completion.is_symlink(), "completion_already_exists")
    with rollout_lock(directory):
        # Repeat preflight under the deployment lock, before the first mutation.
        require(prepare(args) == prepared, "preflight_changed")
        record["status"] = "preflight_record_not_completion"
        write_new(receipt, json.dumps(record, sort_keys=True) + "\n")
        write_new(override, "services:\n  detect-proposal:\n    image: " + args.image + "\n")
        # If Docker fails, leave both the pin and preflight receipt for recovery.
        # Do not retry, remove the pin or restore a database automatically.
        docker(*command)
        after = snapshot(compose + ["-f", str(override)])
        protected_before = {key: row for key, row in before.items() if row["service"] != SERVICE}
        protected_after = {key: row for key, row in after.items() if row["service"] != SERVICE}
        require(protected_before == protected_after, "protected_service_changed")
        backends = [row for row in after.values() if row["service"] == SERVICE]
        require(len(backends) == 1, "post_apply_backend_count_invalid")
        backend = backends[0]
        require(backend["image"] == candidate["id"] and backend["status"] == "running" and
                backend["health"] not in ("unhealthy", "starting") and
                backend["project"] == args.project and backend["directory"] == str(directory) and
                backend["files"] == ",".join([*args_resolved_files(compose), str(override)]),
                "post_apply_backend_not_ready")
        record.update(status="applied_acceptance_pending", applied=True, after=after)
        # Retain immutable preflight receipt; success goes to a separate file.
        write_new(completion, json.dumps(record, sort_keys=True) + "\n")
        print(json.dumps(record, sort_keys=True))


def args_resolved_files(compose):
    return [compose[i + 1] for i, value in enumerate(compose[:-1]) if value == "-f"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project-directory", required=True, type=Path)
    parser.add_argument("--project", required=True)
    parser.add_argument("--compose-file", action="append", required=True)
    parser.add_argument("--backend-container", required=True)
    parser.add_argument("--expected-backend-image", required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--override", required=True)
    parser.add_argument("--receipt", type=Path)
    parser.add_argument("--apply", action="store_true")
    try:
        run(parser.parse_args())
    except (Refused, OSError, ValueError, KeyError, TypeError) as exc:
        diagnostic = str(exc) if isinstance(exc, Refused) else "local_or_metadata_error"
        print(json.dumps({"status": "refused_or_failed", "error_class": diagnostic,
                          "functional_acceptance": "pending"}), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
