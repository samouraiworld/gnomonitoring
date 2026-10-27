# Verify the deployed service before changing it

A successful image build establishes what can be deployed. It does not establish
which image is running. A checkout's HEAD also does not identify an already
running container.

## Observation on 2026-10-27

Read-only public requests to `https://gnomonitoring.samourai.live` established:

| Request | Observed result |
| --- | --- |
| `GET /info` | Enabled chains: `gnoland-1`, `pearl-1`; disabled: `sapphire-1`, `topaz-1` |
| `GET /block_height?chain=gnoland-1` | `last_stored` advanced from `1107852` to `1107899`; second observation at 07:15:44 UTC |
| `GET /api/reports/validators?chain=gnoland-1` | HTTP 200, six validator reports, the existing four period keys |

The API responds and collection advances, but these responses contain no
deployed revision. They do not prove that `/validator`, the #161 operation-time
fix, or the #162 cache fix is deployed. Height is a freshness observation, not a
binary version check.

Source `main` was `128f5bb860342c0082c784e4b8f207b0c55aec40`. Its
[image build succeeded](https://github.com/samouraiworld/gnomonitoring/actions/runs/40370015025).
The latest `Deploy to prod` Actions execution found was the
[2026-07-11 run](https://github.com/samouraiworld/gnomonitoring/actions/runs/29146790672).
This does not exclude later direct server deployments.

The historical workstation record names a server, but no corresponding SSH
alias/user was established in this verification. No SSH session, Docker
inspection, DB query, restart or Telegram action was performed. Actual server
access and container provenance remain the outstanding gate.

## Read-only inspection on the host

Select the directory and compose file actually used by the operator. The existing
deployment workflow refers to `gnomonitoringv2`. Do not substitute the retired
SQLite-era directory. The repository's production file is
`docker-compose-prod.yml`, but the host may use its own file or `COMPOSE_FILE`.

Run the script from a reviewed checkout, passing the host's two explicit paths:

```sh
bash scripts/inspect-deployment.sh /absolute/path/to/gnomonitoringv2 docker-compose-prod.yml
```

It can also be streamed over an existing SSH connection without copying files:

```sh
ssh HOST_ALIAS bash -s -- /absolute/path/to/gnomonitoringv2 docker-compose-prod.yml < scripts/inspect-deployment.sh
```

Use an established operator alias and absolute directory. The directory and at
least one compose file are mandatory. Preserve the deployment's project name
(`-p`/`COMPOSE_PROJECT_NAME`), and pass every compose override in its original
order. For example:

```sh
COMPOSE_PROJECT_NAME=existing-name bash scripts/inspect-deployment.sh /absolute/path/to/gnomonitoringv2 base.yml override.yml
```

The script inherits `COMPOSE_PROJECT_NAME`; it does not discover or set it.
Using a different project name or omitting an override can produce an empty or
misleading selection, so a failed selection does not prove a service is absent.
The script reads Git metadata, compose container IDs, selected
container fields, image IDs, repository digests and OCI revision/source labels.
It includes stopped containers. It does not start, build, pull or remove anything,
run commands inside containers, query the database, or print environment variables
or expanded compose configuration. Docker inspection requires the host's existing
Docker permissions.

Compare `actual_compose_files` and `actual_working_directory` from the running
containers with the paths selected for inspection. A different compose project
can legitimately return no containers; the script reports failure in that case.
Missing OCI revision labels are an evidence gap. Locally built images may have no
repository digest; their image ID alone does not prove the source commit.

## Deployment gate and return path

Record the observation time, checkout revision and tracked-change flag, running
backend/frontend image IDs and digests, OCI revisions, service status, and actual
compose paths. Store the evidence with the release record. Review any unexpected
compose-path, image or chain difference before proceeding.

The current workflow runs bare `docker compose pull` and `docker compose up -d`.
The repository's production compose uses `build:` for the backend and panel, and
its changelog requires an explicit `-f docker-compose-prod.yml`. Inspect the host
first: it may instead have an image-based compose. This PR deliberately leaves
that deployment workflow and production configuration unchanged until the host's
effective setup is known.

Before a later deployment, prepare a single explicit compose/image or source
build strategy and capture the prior image IDs/digests, compose version and a
verified DB backup. Review schema compatibility for the forward and return
paths. Do not roll back an image against an incompatible migrated database.

After deployment, repeat provenance inspection and public height/report checks.
Verify that collection advances on the expected chains and compare the running
revision with the candidate. Only then smoke-test the validator bot and update
its complete existing BotFather command list. The GovDAO bot is a separate bot.

## Tooling validation

```sh
bash -n scripts/inspect-deployment.sh
shellcheck scripts/inspect-deployment.sh
python3 -B -m unittest discover -s scripts -p 'test_inspect_deployment.py' -v
```

The tests use a fake Docker command to verify explicit compose selection, paths
with spaces, read-only calls, error propagation and rejection of malformed
container IDs. They do not certify the production host or a real Docker daemon.
