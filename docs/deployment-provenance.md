# Verify the deployed service before changing it

A successful image build establishes what can be deployed. It does not establish
which image is running. A checkout's HEAD also does not identify an already
running container.

## Historical observations on 2026-10-27

Read-only public requests to `https://gnomonitoring.samourai.live` established:

| Request | Observed result |
| --- | --- |
| `GET /info` | Enabled chains: `gnoland-1`, `pearl-1`; disabled: `sapphire-1`, `topaz-1` |
| `GET /block_height?chain=gnoland-1` | `last_stored` advanced from `1107852` to `1107899`; second observation at 07:15:44 UTC |
| `GET /api/reports/validators?chain=gnoland-1` | HTTP 200, six validator reports, the existing four period keys |

The API responded and collection advanced, but these responses contained no
deployed revision. They alone did not establish whether `/validator`, the #161
operation-time fix, or the #162 cache fix was deployed. Height is a freshness
observation, not a binary version check.

Source `main` was `128f5bb860342c0082c784e4b8f207b0c55aec40`. Its
[image build succeeded](https://github.com/samouraiworld/gnomonitoring/actions/runs/40370015025).
The latest `Deploy to prod` Actions execution found was the
[2026-07-11 run](https://github.com/samouraiworld/gnomonitoring/actions/runs/29146790672).
This does not exclude later direct server deployments.

At the initial public check, the historical workstation record named a server,
but no corresponding SSH alias/user was established. That check performed no
SSH session, Docker inspection, DB query, restart or Telegram action.

### Follow-up read-only image metadata

A partial remote metadata inspection at 09:45 UTC on the same date returned the backend
image's OCI revision label
`693df79a96dff2a50b0fe21fa919186cb192cca0`. Repository ancestry confirms that this
revision includes [#158](https://github.com/samouraiworld/gnomonitoring/pull/158)
(`/validator`) and is the merge of
[#161](https://github.com/samouraiworld/gnomonitoring/pull/161) (operation time).
This is an image metadata/source comparison, not a functional Telegram command
test. No `/validator` smoke test or BotFather update was performed.

Relative to source `128f5bb`, that revision lacks the three commits in
[#162](https://github.com/samouraiworld/gnomonitoring/pull/162): `9e48b82` (cached
slice sorting fix), `723543f` (changelog), and `128f5bb` (merge). The inspection
then stopped when a Postgres image omitted its optional `Config.Labels` map.
The script now handles that absence. A later completed inventory supplied the
scoped result below.

### Completed inventory at 09:57:23 UTC

[Read-only run 40484712221](https://github.com/samouraiworld/gnomonitoring/actions/runs/40484712221)
completed on 2026-10-27 at 09:57:23 UTC. Its exact Compose working-directory
filter selected one backend (`detect-proposal`, running, no configured Docker
healthcheck) and PostgreSQL (running/healthy). The selected directory had no
identifiable Git checkout. No panel matched this scope; its version remained
unverified. PostgreSQL had no OCI revision/source labels.

The backend declared the same OCI revision `693df79` identified above, with image
ID `sha256:863485dda4f089d6287e2c4f60e091e731b9b8301a266eb2bf5844b22045fc6f`
and registry digest
`ghcr.io/samouraiworld/gnomonitoring-server@sha256:b31f17bc76df9af5ff1ad2f674b1ec6af4d6fae50bb6cedf4c9378b5d612fa46`.
This completed the selected scope at that time; it did not inventory other
Compose projects or attest to the service's state hours later. Image labels
declare provenance, without attesting to binary contents or command behavior.
No functional Telegram test was performed. Before a future version claim,
repeat the inventory and compare every relevant backend instance, image ID,
digest and declared revision.

### Repository update after these observations

Later on the same date, [#172](https://github.com/samouraiworld/gnomonitoring/pull/172)
merged dependency fixes and blocking security checks into source `main` at
`555d6e72aff5781d07945e9a350db5bc56023e42`. This repository update was not a
deployment. The historical image label `693df79` does not establish the service's
current revision or show that #162 or #172 is running; repeat the complete
read-only inventory before making that claim.

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

### Dependency gate before release

At the initial 2026-10-27 observation, with source `main` at `128f5bb`, GitHub
reported 93 open Dependabot alerts across the backend, block exporter and panel
manifests. Backend pgx 5.6.0 then had two
critical memory-safety alerts, [#46](https://github.com/samouraiworld/gnomonitoring/security/dependabot/46)
and [#41](https://github.com/samouraiworld/gnomonitoring/security/dependabot/41),
with 5.9.0 listed as patched. PostgreSQL is the backend's actual database path;
that provenance inspection did not establish the affected functions' reachability.
These are historical source observations, not the dependency versions or alert
count after #172, nor an inventory of the deployed images' dependencies.

Dependabot identifies vulnerable dependency versions, not which affected
functions are reachable in a deployed binary. Some alerts repeat across Go
modules or affect development tooling and server modes absent from the static
panel. Conversely, manifest alerts do not cover every vulnerability in the
container's OS or Go toolchain. Match this triage to the observed deployed
images, remediate applicable critical/high findings, and record the evidence
before release. A passing Memba dependency audit covers Memba, not Gnomonitoring.

At source `128f5bb`, the image workflow ran Trivy with `exit-code: 0`, and the Go
workflows did not run `govulncheck`. Those historical green results did not
establish a clean dependency or image security assessment.

Since #172, the required `Security gate` blocks affected imported Go packages
and called symbols (including tests), HIGH/CRITICAL findings in the panel's
complete npm dependency tree, and HIGH/CRITICAL OS/library findings in the actual
candidate backend and panel runtime images. Scanner failures also block the
gate; there are no advisory exceptions or exemptions for unfixed findings. See
[Security checks and publication policy](security-ci.md) for the exact scope.
A successful candidate check does not attest to an image already deployed, and
these observations do not establish a production exploit.

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
container IDs. A local Go toolchain also renders the captured container/image
templates with strict map lookups against absent, null, empty and populated
label fixtures, and with/without healthchecks. It uses the standard library only;
no Go module download is needed. The tests do not certify the production host or
a real Docker daemon.
