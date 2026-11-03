"""Fixed production target for the manually dispatched SSH release runner."""
import argparse
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import re
import sys

import rollout_backend as rollout


FIELDS = {"operation", "image", "revision", "backend_container", "expected_backend_image", "release_id"}


def validate_request(request):
    rollout.require(isinstance(request, dict) and set(request) == FIELDS, "request_fields_invalid")
    rollout.require(all(isinstance(value, str) for value in request.values()), "request_types_invalid")
    rollout.require(request["operation"] in ("plan", "pull", "apply"), "operation_invalid")
    rollout.require(bool(rollout.IMAGE.fullmatch(request["image"])), "digest_reference_required")
    rollout.require(bool(rollout.REVISION.fullmatch(request["revision"])), "full_revision_required")
    rollout.require(bool(rollout.CONTAINER_ID.fullmatch(request["backend_container"])), "backend_id_invalid")
    rollout.require(bool(rollout.IMAGE_ID.fullmatch(request["expected_backend_image"])), "previous_image_id_required")
    rollout.require(bool(re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", request["release_id"])), "release_id_invalid")


def execute(request):
    validate_request(request)
    directory = (Path.home() / "gnomonitoringv2").resolve(strict=True)
    backend = rollout.inspect_container(request["backend_container"])
    rollout.require(backend["project"] == "gnomonitoringv2" and
                    backend["service"] == rollout.SERVICE and
                    backend["directory"] == str(directory) and
                    backend["image"] == request["expected_backend_image"] and
                    backend["status"] == "running", "remote_target_mismatch")
    rollout.require(isinstance(backend["files"], str) and bool(backend["files"]), "remote_compose_files_missing")
    files = backend["files"].split(",")
    rollout.require(all(Path(path).is_absolute() for path in files), "remote_compose_paths_invalid")
    if request["operation"] == "pull":
        # Explicit preparation only: never pull any other service or mutable tag.
        rollout.docker("pull", request["image"])
    basename = "backend-release-" + request["release_id"]
    args = argparse.Namespace(project_directory=directory, project="gnomonitoringv2",
        compose_file=files, backend_container=request["backend_container"],
        expected_backend_image=request["expected_backend_image"], image=request["image"],
        revision=request["revision"], override=basename + ".yml",
        receipt=directory / (basename + ".receipt.json"), apply=request["operation"] == "apply")
    output = io.StringIO()
    with redirect_stdout(output):
        rollout.run(args)
    record = json.loads(output.getvalue())
    record["transport_operation"] = request["operation"]
    print(json.dumps(record, sort_keys=True))


def main(request):
    try:
        execute(request)
    except (rollout.Refused, OSError, ValueError, KeyError, TypeError) as exc:
        diagnostic = str(exc) if isinstance(exc, rollout.Refused) else "remote_metadata_or_local_error"
        print(json.dumps({"status": "refused_or_failed", "error_class": diagnostic,
                          "functional_acceptance": "pending"}), file=sys.stderr)
        return 1
    return 0
