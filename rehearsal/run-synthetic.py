#!/usr/bin/env python3
"""Exercise InitDB on new synthetic volumes only; no backup/restore capability."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import stat
import subprocess
import sys
import time

BASE = "92ed63fe7eb5ec54327aff36dc2bcaf2e6f24968"
DB_SOURCE_SHA = "b89ab62f218255680065eaca18db81bfe86c53ded9667bb4c839e59eeb43eff9"
ROOT = Path(__file__).resolve().parent.parent
COMPOSE = ROOT / "rehearsal/compose.synthetic.yml"
HELPER_FILES = {"backend/cmd/rehearse-initdb/" + name for name in ["main.go", "guard.go", "guard_test.go", "fd_linux.go", "fd_darwin.go"]}


class Refused(Exception):
    pass


def check_output_directory(path):
    directory = Path(path)
    if not directory.is_absolute() or directory.resolve() != directory:
        raise Refused("private_output_directory_must_be_absolute_without_symlinks")
    info = directory.stat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o700 or info.st_uid != os.getuid() or any(directory.iterdir()):
        raise Refused("empty_private_owned_output_directory_required")
    return directory


def approved_socket(requested=None):
    expected = Path.home() / ".docker/run/docker.sock" if platform.system() == "Darwin" else Path("/var/run/docker.sock")
    if platform.system() not in {"Darwin", "Linux"} or (requested and Path(requested) != expected):
        raise Refused("approved_local_unix_docker_socket_required")
    info = expected.stat()
    if not stat.S_ISSOCK(info.st_mode) or info.st_uid not in {0, os.getuid()}:
        raise Refused("approved_local_docker_socket_unavailable")
    return expected


def reject_remote_context(environment):
    for key, value in environment.items():
        if value and (key in {"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "DOCKER_SSH_COMMAND", "BUILDKIT_HOST"} or key.startswith("BUILDX_")):
            raise Refused("inherited_docker_context_refused")


def validate_config(config, project):
    services = config.get("services", {})
    if set(services) != {"postgres", "initdb"} or set(config.get("volumes", {})) != {"pgdata", "socket", "private_logs"}:
        raise Refused("unexpected_synthetic_services_or_volumes")
    for name, service in services.items():
        if service.get("network_mode") != "none" or service.get("user") != "999:999" or service.get("ports") or service.get("privileged") or service.get("pid") or service.get("devices"):
            raise Refused("synthetic_runtime_fence_required")
        if service.get("image") != f"{project}-{'postgres' if name == 'postgres' else 'initdb'}:local-only":
            raise Refused("fresh_local_image_names_required")
        if service.get("cap_add") or service.get("cap_drop") != ["ALL"]:
            raise Refused("capability_fence_required")
        if service.get("env_file") or service.get("secrets"):
            raise Refused("external_environment_or_secrets_refused")
        environment = service.get("environment") or {}
        expected = {"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB", "REHEARSAL_CLONE_ID"} if name == "postgres" else set()
        if set(environment) != expected:
            raise Refused("unexpected_runtime_environment_refused")
        if name == "postgres" and (environment["POSTGRES_USER"] != "rehearsal_admin" or environment["POSTGRES_DB"] != "postgres"):
            raise Refused("synthetic_role_and_database_required")
        if name == "initdb" and not service.get("read_only"):
            raise Refused("read_only_initdb_root_required")
        expected_mounts = {
            ("socket", "/run/rehearsal-postgres", name == "initdb"),
            ("private_logs", "/run/rehearsal-private", False),
        }
        if name == "postgres":
            expected_mounts.add(("pgdata", "/var/lib/postgresql/data", False))
        mounts = service.get("volumes") or []
        actual_mounts = set()
        for mount in mounts:
            if mount.get("type") != "volume" or mount.get("bind") or mount.get("tmpfs") or mount.get("volume", {}).get("subpath"):
                raise Refused("resolved_host_or_unknown_mount_refused_before_start")
            actual_mounts.add((mount.get("source"), mount.get("target"), bool(mount.get("read_only"))))
        if actual_mounts != expected_mounts or len(mounts) != len(expected_mounts) or service.get("tmpfs") or service.get("volumes_from"):
            raise Refused("exact_resolved_fresh_mounts_required_before_start")
    for key, suffix in [("pgdata", "pgdata"), ("socket", "socket"), ("private_logs", "private-logs")]:
        volume = config["volumes"][key]
        if volume.get("name") != f"{project}-{suffix}" or volume.get("external") or volume.get("driver_opts") or volume.get("driver") not in {None, "local"}:
            raise Refused("fresh_owned_volumes_required")


def validate_volume_fence(info, expected, project):
    if info.get("Name") != expected or info.get("Driver") != "local" or info.get("Options") or info.get("Scope") != "local" or (info.get("Labels") or {}).get("com.docker.compose.project") != project:
        raise Refused("actual_local_owned_volume_required")


def validate_container_fence(info, project):
    if info.get("network_mode") != "none" or any((info.get("ports") or {}).values()) or info.get("privileged") or info.get("pid_mode"):
        raise Refused("actual_container_fence_refused")
    if info.get("user") != "999:999" or info.get("cap_drop") != ["ALL"] or info.get("cap_add") or not {"no-new-privileges", "no-new-privileges:true"}.intersection(info.get("security_opt") or []):
        raise Refused("actual_container_capabilities_refused")
    expected = {f"{project}-pgdata", f"{project}-socket", f"{project}-private-logs"}
    for mount in info.get("mounts", []):
        if mount.get("Type") == "volume" and mount.get("Name") not in expected:
            raise Refused("unexpected_existing_volume_refused")
        if mount.get("Type") == "bind":
            raise Refused("unexpected_host_mount_refused")


class Runner:
    def __init__(self, output, docker_socket=None):
        reject_remote_context(os.environ)
        socket = approved_socket(docker_socket)
        self.output = output
        self.sequence = 0
        self.clone_id = secrets.token_hex(16)
        self.project = "gnomon-rehearsal-" + self.clone_id
        self.environment = {key: value for key, value in os.environ.items() if key in {"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL"}}
        configuration = output / "docker-config"
        configuration.mkdir(mode=0o700)
        settings = {"auths": {}, "currentContext": "default"}
        if platform.system() == "Darwin":
            # Isolate credentials/context without hiding Desktop's installed
            # compose/buildx plugins. Never copy the user's Docker config.
            settings["cliPluginsExtraDirs"] = ["/Applications/Docker.app/Contents/Resources/cli-plugins"]
        (configuration / "config.json").write_text(json.dumps(settings) + "\n")
        os.chmod(configuration / "config.json", 0o600)
        self.environment.update({"DOCKER_CONFIG": str(configuration), "BUILDX_CONFIG": str(configuration / "buildx")})
        self.environment.update({"REHEARSAL_PROJECT": self.project, "REHEARSAL_CLONE_ID": self.clone_id,
                                 "REHEARSAL_SYNTHETIC_ADMIN_PASSWORD": secrets.token_hex(24), "GIT_OPTIONAL_LOCKS": "0"})
        self.docker = ["docker", "--host", "unix://" + str(socket)]
        self.compose = self.docker + ["compose", "--project-name", self.project, "--file", str(COMPOSE), "--project-directory", str(COMPOSE.parent)]

    def command(self, arguments, allowed=(0,), timeout=600):
        if arguments[0] == "docker" and arguments[:3] != self.docker:
            raise Refused("unpinned_docker_endpoint_refused")
        self.sequence += 1
        completed = subprocess.run(arguments, cwd=ROOT, env=self.environment, capture_output=True, timeout=timeout, check=False)
        for stream, content in [("stdout", completed.stdout), ("stderr", completed.stderr)]:
            path = self.output / f"{self.sequence:02d}-{stream}.private"
            with path.open("xb") as handle:
                os.chmod(path, 0o600)
                handle.write(content)
        if completed.returncode not in allowed:
            raise Refused("synthetic_step_failed_see_private_logs")
        return completed.stdout.decode("utf-8"), completed.returncode

    def fresh_volumes(self):
        for suffix in ["pgdata", "socket", "private-logs"]:
            # A missing volume must be the only reason this inspect fails.
            output, status = self.command(self.docker + ["volume", "ls", "--format", "{{.Name}}", "--filter", f"name=^{self.project}-{suffix}$"])
            if status != 0 or output.strip():
                raise Refused("existing_volume_refused")
        output, _ = self.command(self.docker + ["ps", "--all", "--quiet", "--filter", f"label=com.docker.compose.project={self.project}"])
        if output.strip():
            raise Refused("existing_project_refused")

    def inspect_fence(self, container):
        template = '{"network_mode":{{json .HostConfig.NetworkMode}},"ports":{{json .NetworkSettings.Ports}},"privileged":{{json .HostConfig.Privileged}},"pid_mode":{{json .HostConfig.PidMode}},"user":{{json (index .Config "User")}},"cap_add":{{json (index .HostConfig "CapAdd")}},"cap_drop":{{json .HostConfig.CapDrop}},"security_opt":{{json (index .HostConfig "SecurityOpt")}},"mounts":{{json .Mounts}}}'
        output, _ = self.command(self.docker + ["inspect", "--format", template, container])
        validate_container_fence(json.loads(output), self.project)

    def inspect_volume_fence(self):
        template = '{"Name":{{json .Name}},"Driver":{{json .Driver}},"Options":{{json (index . "Options")}},"Scope":{{json .Scope}},"Labels":{{json (index . "Labels")}}}'
        for suffix in ["pgdata", "socket", "private-logs"]:
            name = f"{self.project}-{suffix}"
            output, _ = self.command(self.docker + ["volume", "inspect", "--format", template, name])
            validate_volume_fence(json.loads(output), name, self.project)

    def wait_postgres(self, container):
        deadline = time.monotonic() + 90
        template = '{"running":{{json .State.Running}},"health":"{{with index .State "Health"}}{{.Status}}{{else}}missing{{end}}"}'
        while time.monotonic() < deadline:
            output, _ = self.command(self.docker + ["inspect", "--format", template, container])
            state = json.loads(output)
            if not state.get("running") or state.get("health") in {"unhealthy", "missing"}:
                raise Refused("synthetic_postgres_startup_failed")
            if state.get("health") == "healthy":
                return
            time.sleep(1)
        raise Refused("synthetic_postgres_readiness_timed_out")

    def start_initdb(self, container, allowed=(0, 3)):
        output, _ = self.command(self.docker + ["start", "--attach", container], allowed=allowed)
        status, _ = self.command(self.docker + ["inspect", "--format", "{{.State.ExitCode}}", container])
        return json.loads(output), int(status.strip())

    def pin_sources(self):
        # Check the actual working-tree bytes, not only git diff/index flags.
        tree, _ = self.command(["git", "ls-tree", "-rz", "--full-tree", BASE, "--", "backend"])
        expected = {}
        for entry in filter(None, tree.split("\0")):
            metadata, path = entry.split("\t", 1)
            mode, kind, digest = metadata.split()
            if kind != "blob" or mode not in {"100644", "100755"}:
                raise Refused("unsupported_baseline_build_context_entry")
            expected[path] = digest
            file = ROOT / path
            if file.is_symlink() or not file.is_file():
                raise Refused("baseline_build_context_file_changed")
            content = file.read_bytes()
            actual = hashlib.sha1(b"blob " + str(len(content)).encode() + b"\0" + content).hexdigest()
            if actual != digest:
                raise Refused("baseline_build_context_file_changed")
        tracked, _ = self.command(["git", "ls-files", "-z", "--", "backend"])
        others, _ = self.command(["git", "ls-files", "--others", "--exclude-standard", "-z", "--", "backend"])
        ignored, _ = self.command(["git", "ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--", "backend"])
        paths = set(filter(None, (tracked + others + ignored).split("\0")))
        if paths - set(expected) - HELPER_FILES:
            raise Refused("unexpected_tracked_untracked_or_ignored_build_file")
        helper_paths = {str(path.relative_to(ROOT)) for path in (ROOT / "backend/cmd/rehearse-initdb").rglob("*") if not path.is_dir()}
        if helper_paths != HELPER_FILES or any((ROOT / path).is_symlink() for path in helper_paths):
            raise Refused("explicit_helper_source_manifest_required")
        return {path: hashlib.sha256((ROOT / path).read_bytes()).hexdigest() for path in sorted(helper_paths)}

    def execute(self):
        if hashlib.sha256((ROOT / "backend/internal/database/db_init.go").read_bytes()).hexdigest() != DB_SOURCE_SHA:
            raise Refused("database_source_pin_mismatch")
        helper_sources = self.pin_sources()
        operating_system, _ = self.command(self.docker + ["info", "--format", "{{.OSType}}"])
        if operating_system.strip() != "linux":
            raise Refused("linux_local_engine_required")
        self.fresh_volumes()
        config, _ = self.command(self.compose + ["config", "--format", "json"])
        validate_config(json.loads(config), self.project)
        created = False
        postgres_id = None
        try:
            created = True
            self.command(self.compose + ["build", "postgres", "initdb"], timeout=1200)
            self.command(self.compose + ["create", "postgres"])
            self.inspect_volume_fence()
            postgres, _ = self.command(self.compose + ["ps", "--all", "--quiet", "postgres"])
            postgres_id = postgres.strip()
            self.inspect_fence(postgres_id)
            self.command(self.docker + ["start", postgres_id])
            self.wait_postgres(postgres_id)
            self.command(self.compose + ["create", "initdb"])
            initdb, _ = self.command(self.compose + ["ps", "--all", "--quiet", "initdb"])
            self.inspect_fence(initdb.strip())
            first, first_status = self.start_initdb(initdb.strip())
            if first_status != 3 or first.get("status") != "completed_requires_deletion_review" or first.get("rows_removed") != {"daily_participations": 1}:
                raise Refused("synthetic_cleanup_measurement_mismatch")
            second, second_status = self.start_initdb(initdb.strip())
            if second_status != 0 or second.get("status") != "completed" or second.get("rows_removed") or first.get("after") != second.get("before") or second.get("before") != second.get("after"):
                raise Refused("synthetic_idempotence_failed")
            # Corrupt only this fresh synthetic marker and prove refusal before
            # snapshot/migration. This is not a restore or a production query.
            self.command(self.compose + ["exec", "--no-TTY", "postgres", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-h", "/run/rehearsal-postgres", "-U", "rehearsal_admin", "-d", "gnomonitoring_rehearsal_" + self.clone_id,
                                         "-c", "UPDATE rehearsal_guard.clone_marker SET clone_id='mismatched-synthetic-marker'"])
            refused, refusal_status = self.start_initdb(initdb.strip(), allowed=(0, 1))
            if refusal_status != 1 or refused.get("error_class") != "clone_marker_missing_or_mismatched" or refused.get("before"):
                raise Refused("synthetic_marker_refusal_failed")
            report = {"format_version": 1, "synthetic_only": True, "database_base_revision": BASE,
                      "database_source_sha256": DB_SOURCE_SHA, "project": self.project, "network_mode": "none",
                      "helper_working_tree_source_sha256": helper_sources,
                      "first_pass": first, "second_pass": second, "marker_mismatch_refused_before_migration": True, "production_data_used": False,
                      "restore_performed": False, "image_publication_performed": False}
        finally:
            if created:
                self.command(self.compose + ["logs", "--no-color", "postgres"], allowed=(0, 1))
                if not postgres_id:
                    container, _ = self.command(self.compose + ["ps", "--all", "--quiet", "postgres"], allowed=(0, 1))
                    postgres_id = container.strip() or None
                if postgres_id:
                    self.command(self.docker + ["cp", f"{postgres_id}:/run/rehearsal-private", str(self.output / "database-private-logs")], allowed=(0, 1))
                self.command(self.compose + ["down", "--volumes"], timeout=120)
                self.command(self.docker + ["image", "rm", f"{self.project}-postgres:local-only", f"{self.project}-initdb:local-only"], allowed=(0, 1))
                self.fresh_volumes()
                images, _ = self.command(self.docker + ["image", "ls", "--format", "{{.Repository}}:{{.Tag}}", "--filter", f"reference={self.project}-*:local-only"])
                if images.strip():
                    raise Refused("synthetic_image_cleanup_incomplete")
        report["owned_resources_cleanup_verified"] = True
        (self.output / "synthetic-summary.json").write_text(json.dumps(report, indent=2) + "\n")
        os.chmod(self.output / "synthetic-summary.json", 0o600)
        return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute-synthetic", action="store_true")
    parser.add_argument("--private-output-dir")
    parser.add_argument("--docker-socket")
    arguments = parser.parse_args()
    if not arguments.execute_synthetic:
        print(json.dumps({"status": "tools_only", "action": "none", "requires": "--execute-synthetic and empty private output directory"}))
        return 0
    try:
        if not arguments.private_output_dir:
            raise Refused("explicit_private_output_directory_required")
        report = Runner(check_output_directory(arguments.private_output_dir), arguments.docker_socket).execute()
        print(json.dumps({"status": "synthetic_rehearsal_completed", "network_mode": report["network_mode"],
                          "first_pass_deleted_rows_requiring_review": report["first_pass"]["rows_removed"],
                          "second_pass_idempotent": True, "production_data_used": False}))
        return 0
    except (Refused, OSError, ValueError, subprocess.TimeoutExpired) as error:
        # Never echo subprocess output, SQL DETAIL, DSNs or exception text.
        classification = str(error) if isinstance(error, Refused) else "synthetic_harness_failed_see_private_logs"
        print(json.dumps({"status": "refused_or_failed", "error_class": classification}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
