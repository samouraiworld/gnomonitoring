# Backend release handoff: verifier and first-incident alerts

This is a preparation record, not a record of deployment. Keep integration
[#191](https://github.com/samouraiworld/gnomonitoring/pull/191) in draft until the
real Memba Clerk-session check is recorded. Documentation work can be reviewed
separately without changing that candidate.

## Candidate and evidence

- Base: `977f9daa498d5200d95deef4a348335112d0fa2f`.
- Integration: `2053a5ba32ac1ea137ea69653dfb4ce36a82f35d`.
- Auth #189: `078b0f4257c7599d78a7ff3961dc423be10dfae5`.
- Alerts #163: `759ec54801a051aaff3adc32d44da0b114faf27e`.
- Hosted [Go race tests](https://github.com/samouraiworld/gnomonitoring/actions/runs/41523022775),
  [lint](https://github.com/samouraiworld/gnomonitoring/actions/runs/41523022850)
  and [all seven Security jobs](https://github.com/samouraiworld/gnomonitoring/actions/runs/41523022783)
  passed on the integrated head, including the actual runtime image scans.
- Three existing synthetic integration/rehearsal failures remain recorded in #191.
  Two report `baseline_build_context_file_changed`; the third reports a CLI exit
  mismatch without a retained detailed diagnostic. Do not claim a proved cause
  for that third failure, or weaken the frozen baseline to make it green.
- A real Memba session has not yet been checked against the new verifier. A
  screenshot of a signed-in account establishes the frontend session, not that
  the candidate accepts it. Wallet connectivity does not supply a Clerk session.

The real check must use the candidate verifier and the existing backend Clerk
instance configuration, with dev-mode bypass disabled. Keep the session token,
key and identity local to the test environment. Verify signature/claims and
middleware acceptance with a handler that writes no application data, then
refusal without a token (403), for malformed input (403) and for an altered
signature (401). Return only commit, UTC date, session provenance and results.
The auth package in the integration is byte-identical to #189, so a result on
that exact auth head applies to the verifier; retest if its code changes.

## November 3 panel finding and combined candidate

Fresh scans on documentation PR #192 found two HIGH libexpat findings in the
panel runtime that were absent from the recorded November 2 scan. Panel
[#193](https://github.com/samouraiworld/gnomonitoring/pull/193), commit
`b146d7cb489a95609187a76813039abc3ddd6fd6`, pins `libexpat=2.10.0-r0` in the
existing APK upgrade. A local negative control detects CVE-2026-117610 and
CVE-2026-118370 before the change; the patched image has zero HIGH/CRITICAL
findings with the same Trivy v0.76.1 DB and passes the hosted panel image scan.
Its frontend files and nginx configuration are unchanged. This candidate-image
finding does not establish a vulnerability in the separately hosted live panel.

The release-candidate branch combines #191, #192 and #193 while leaving all
three reference heads unchanged. Its backend runtime code matches `2053a5b`
and its panel source matches `b146d7c`. Validate the combined candidate's required
checks before merging; the separate main-based branches each lack the other
image's fix and cannot pass the complete gate independently. The original real
Memba session test requirement remains; this combination does not fulfil it.

## Publication is currently held

On November 3, the GitHub Actions API reports `Build_backend_image.yml`
(workflow ID `186877706`, **Docker Build & Push**) as `disabled_manually`.
A merge therefore does not automatically publish an image. This observation
has not changed the workflow's state or triggered a run.

After the compatibility check and final review, refresh the candidate/base and
required checks. Preserve the two original histories when integrating #191.
Resolve the publication hold as an explicit release step; do not silently assume
it has already been removed. Run the existing publication workflow only on the
reviewed merged commit. Its [source](../.github/workflows/Build_backend_image.yml)
runs security checks, builds the image, scans that actual image ID and only then
logs into the registry and publishes that same image. Do not replace it with an
unscanned local push, a scanner exception, or an older image carrying a new tag.

Record the successful publication run, source commit, registry digest and OCI
revision. The published merge commit will differ from the PR head; verify that
its source tree contains the tested candidate and any reviewed intervening work.
Use the digest for rollout, not the movable `latest` or `main` tag.

## Last production observation and backup prerequisite

The [read-only inspection on November 2 at 20:56:29 UTC](https://github.com/samouraiworld/gnomonitoring/actions/runs/41523756511)
recorded:

- Compose project `gnomonitoringv2`, service `detect-proposal`.
- Project directory ending in `/gnomonitoringv2`, effective file `docker-compose.yml`.
- Running backend revision `693df79a96dff2a50b0fe21fa919186cb192cca0`.
- Backend image `ghcr.io/samouraiworld/gnomonitoring-server@sha256:b31f17bc76df9af5ff1ad2f674b1ec6af4d6fae50bb6cedf4c9378b5d612fa46`.
- PostgreSQL running and healthy. No Git checkout was identifiable in that project.

These are dated observations, not an assurance that the host is unchanged.
Repeat [deployment inspection](deployment-provenance.md) before making changes.
Retain every effective compose file in order and the actual project name; do not
substitute the repository's example compose for the operator's live file.

The existing release procedure also requires a verified recoverable DB backup
and schema compatibility for both directions. No production backup/restore proof
is established by this handoff. [Backup PR #190](https://github.com/samouraiworld/gnomonitoring/pull/190)
is separate work and lists production validation as pending; do not treat its
local synthetic tests as a production recovery test or install it as part of this
backend rollout. Review the actual final schema diff before release and do not
roll an old binary back across an incompatible database migration.

## Targeted backend rollout and return

`scripts/rollout_backend.py` implements the command boundary below. It defaults
to a read-only plan and never downloads an image. Pull and scan the published
digest separately, verify the real-session/recovery prerequisites above, then
run the plan using the freshly inspected full backend container/image IDs:

```bash
python3 /path/to/reviewed/scripts/rollout_backend.py \
  --project-directory "$PWD" --project gnomonitoringv2 \
  --compose-file docker-compose.yml \
  --backend-container "$CURRENT_BACKEND_CONTAINER_ID" \
  --expected-backend-image "$CURRENT_BACKEND_IMAGE_ID" \
  --image "$RELEASE_IMAGE" --revision "$FULL_MERGED_COMMIT" \
  --override backend-release.override.yml
```

Repeat `--compose-file` for **every** currently effective file in order. The
tool refuses a mismatch with container labels, a mutable tag, missing local
digest, wrong OCI source/revision, a changed backend, an unhealthy/stopped
existing service or an existing override. It supports one backend replica.
Planning reads selected metadata only; it never requests environment variables,
expanded Compose config or database contents.

Only after reviewing that plan, add `--apply --receipt /private/path/rollout.json`
to the same command. Apply takes a project lock, repeats preflight, creates a
new mode-0600 persistent pin and immutable preflight receipt, and runs exactly
the backend-only Compose operation below. Existing files/symlinks are never
replaced. It checks every other selected service's identity, start time, image
and state after apply. A separate `.applied.json` receipt means those immediate
checks passed; **functional and authenticated production acceptance remain
pending**, including repeated collection checks after startup.

On timeout, Docker failure or a changed protected service, stop and inspect the
actual state. The tool does not retry, remove the pin, roll back automatically
or restore a database. The preflight receipt alone is not evidence that apply
did or did not occur. For a compatible-schema image return, use a **new**
override basename, retain the current effective files (including the release
pin), and pass the freshly inspected backend ID/image with the recorded previous
published digest/revision. Preserve that complete override chain afterwards.
The unscoped workflow described next remains unsuitable for this release.

The existing **Deploy to prod** workflow performs unscoped `docker compose pull`
and `docker compose up -d`. Do not use it for this backend-only release: it could
update PostgreSQL or other services at the same time.

The following is an operator procedure for after the prerequisites above. It has
not been executed. Run it in the verified production project directory, with the
same compose project, base files, overrides and environment as the running stack.
The example matches the single base file recorded above; if inspection finds
additional overrides, retain them before the release override in every command.

Set `RELEASE_IMAGE` to the reviewed published digest and `ROLLBACK_IMAGE` to the
freshly recorded previous backend digest. Neither variable is a secret. Validate
both references before use:

```bash
[[ "$RELEASE_IMAGE" =~ ^ghcr.io/samouraiworld/gnomonitoring-server@sha256:[0-9a-f]{64}$ ]]
[[ "$ROLLBACK_IMAGE" =~ ^ghcr.io/samouraiworld/gnomonitoring-server@sha256:[0-9a-f]{64}$ ]]
```

Stop if either check fails. Choose a new, persistent release-override file in the
operator's deployment directory and ensure it does not already exist:

```bash
release_override="$PWD/backend-release.override.yml"
(umask 077; set -o noclobber; printf 'services:\n  detect-proposal:\n    image: %s\n' "$RELEASE_IMAGE" > "$release_override")
```

Stop on any failed command. Pull only the selected backend image. Inspect the
local image's revision/digest and compare them with the publication evidence
before starting it:

```bash
docker pull "$RELEASE_IMAGE"
docker image inspect "$RELEASE_IMAGE" \
  --format '{{index .Config.Labels "org.opencontainers.image.revision"}} {{json .RepoDigests}}'
docker compose --project-name gnomonitoringv2 \
  -f docker-compose.yml -f "$release_override" \
  up -d --no-deps --no-build --pull never detect-proposal
```

Keep that override as part of the effective deployment configuration for all
subsequent commands, inspection and any future deployment workflow. An old
unscoped deployment that omits it can revert the image selection. Do not print
expanded `docker compose config`, environment variables or the mounted config
file: they can contain credentials. Restarting the backend interrupts its process;
do not describe this procedure as zero downtime.

After rollout, inspect the full effective compose selection again, confirm the
running image/revision and unchanged PostgreSQL container ID/start time, and
check collection advancing on the expected chains. Confirm the public reports
and a real Memba authenticated read, plus unauthenticated/invalid-session refusals.
Do not create webhooks, send Telegram/Discord messages or change bot commands as
an implicit smoke test. Bot notification testing is a separate operation.

If acceptance fails and the schema review permits an image return, restore the
recorded backend digest in the same override and recreate only that service:

```bash
printf 'services:\n  detect-proposal:\n    image: %s\n' "$ROLLBACK_IMAGE" > "$release_override"
docker pull "$ROLLBACK_IMAGE"
docker compose --project-name gnomonitoringv2 \
  -f docker-compose.yml -f "$release_override" \
  up -d --no-deps --no-build --pull never detect-proposal
```

Repeat provenance and functional checks after the return. Keep the existing
backend auth settings and panel build throughout this release/return; switching
identity provider is not an image rollback.
