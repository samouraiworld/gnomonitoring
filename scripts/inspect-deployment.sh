#!/usr/bin/env bash
# Read-only container provenance. Never dump compose configuration or environment.
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "Usage: $0 PROJECT_DIRECTORY COMPOSE_FILE [OVERRIDE_FILE ...]" >&2
  exit 2
fi

project_dir=$(cd "$1" && pwd -P)
shift
compose_args=()
for path in "$@"; do
  case "$path" in
    /*) compose_file=$path ;;
    *) compose_file=$project_dir/$path ;;
  esac
  if [[ ! -f "$compose_file" ]]; then
    echo "Compose file does not exist: $compose_file" >&2
    exit 2
  fi
  compose_args+=(-f "$compose_file")
done
command -v docker >/dev/null

printf 'format_version=1\nobserved_at=%s\nproject_directory=%s\n' \
  "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$project_dir"
for ((i = 1; i < ${#compose_args[@]}; i += 2)); do
  printf 'selected_compose_file=%s\n' "${compose_args[$i]}"
done

# Checkout HEAD is context only: it is not the running image's revision.
if command -v git >/dev/null && git -C "$project_dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  printf 'checkout_revision=%s\n' "$(git -C "$project_dir" rev-parse HEAD)"
  if [[ -n "$(GIT_OPTIONAL_LOCKS=0 git -C "$project_dir" status --porcelain --untracked-files=no)" ]]; then
    echo 'checkout_tracked_changes=true'
  else
    echo 'checkout_tracked_changes=false'
  fi
else
  echo 'checkout_revision=unavailable'
fi

# Explicit -f avoids selecting a retired/default/devnet compose accidentally.
# ps includes stopped containers so a crashloop is not mistaken for no service.
container_ids=$(docker compose --project-directory "$project_dir" "${compose_args[@]}" ps --all --quiet)
if [[ -z "$container_ids" ]]; then
  echo 'No containers found for the selected compose project.' >&2
  exit 1
fi

for container in $container_ids; do
  # Docker IDs only; do not accept arbitrary output as command options.
  if [[ ! "$container" =~ ^[0-9a-f]{12,64}$ ]]; then
    echo 'Unexpected container ID returned by compose.' >&2
    exit 1
  fi
  echo '--- container ---'
  docker inspect --format 'container_id={{.Id}}
container_name={{.Name}}
configured_image={{.Config.Image}}
image_id={{.Image}}
status={{.State.Status}}
started_at={{.State.StartedAt}}
health={{if .State.Health}}{{.State.Health.Status}}{{else}}not_configured{{end}}
compose_project={{index .Config.Labels "com.docker.compose.project"}}
compose_service={{index .Config.Labels "com.docker.compose.service"}}
actual_compose_files={{index .Config.Labels "com.docker.compose.project.config_files"}}
actual_working_directory={{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$container"
  image_id=$(docker inspect --format '{{.Image}}' "$container")
  docker image inspect --format 'image_id={{.Id}}
repo_digests={{json .RepoDigests}}
image_revision={{if .Config.Labels}}{{index .Config.Labels "org.opencontainers.image.revision"}}{{end}}
image_source={{if .Config.Labels}}{{index .Config.Labels "org.opencontainers.image.source"}}{{end}}' "$image_id"
done

echo 'Revision labels and digests identify images; a missing revision is not proof of the checkout running.'
