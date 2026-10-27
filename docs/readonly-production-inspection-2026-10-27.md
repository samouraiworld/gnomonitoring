# Temporary read-only production inspection — DO NOT MERGE

This branch deliberately replaces the existing deploy workflow with an
inspection-only variant. It is for one reviewed `workflow_dispatch` run on
`chore/monitoring-readonly-inspection-labels-20261027`, not a change to the default
branch. Open the PR as a draft, inspect the complete workflow diff and its
SHA-256 before any dispatch, then close the PR after collecting the result.
Do not merge it. No SSH connection or workflow run was performed while
preparing this branch.

The job is guarded by the exact repository, branch and event; uses native SSH
without third-party actions; and has only `contents: read`. The existing SSH
secrets are injected at runtime, never exported from GitHub. The identity is
written under `RUNNER_TEMP` with mode 600, removed on exit, and unset from the
SSH client's environment. The complete SSH session is bounded to 60 seconds;
the job timeout is two minutes. No arbitrary command input is exposed.

The three public host keys were copied from the operator's existing trusted
`known_hosts` entries. They are pinned to the logical alias
`gnomonitoring-prod-inspection`; no server address, username or private identity
is included in this PR. SSH uses strict host checking, no host-key updates,
no identity agent and no password/interactive fallback. A changed or missing
host key must stop the inspection, never weaken verification.

The remote script uses only the documented `$HOME/gnomonitoringv2` directory.
It reports its checkout SHA and a tracked-change boolean, available Compose
basenames, and Docker metadata for containers whose Compose working-directory
label exactly matches that project. Only container identity/state, image ID,
RepoDigests, OCI revision/source and actual Compose/project labels are printed.
Git optional locks and filesystem-monitor hooks are disabled for status.

There is no Compose configuration rendering, environment/configuration dump,
file upload, Docker exec/logs, database query, image pull, service restart,
deployment or Telegram operation. A missing or different working-directory
label fails closed; the script does not fall back to other projects or an
unscoped container listing. Empty image provenance remains unavailable evidence.

## Local verification

```sh
actionlint .github/workflows/main.yml
python3 -B -m unittest discover -s .github/scripts -p 'test_readonly_inspection.py' -v
git diff --check
```

The shell blocks also pass `bash -n` and `shellcheck -s bash`. Tests use mocked
SSH/Git/Docker executables and a dummy identity only; they verify the scoped
read-only command boundary, missing/malformed project results, SSH options,
temporary-key permissions and cleanup on success/failure. No real secret or
remote service is accessed by the tests.

## Remaining runtime unknowns

The current GitHub secret availability, remote authentication, network reachability
and Docker access are not established. Repository/environment protections and
required approvals still apply to a future dispatch. A successful inspection
would establish image/container provenance at that time; it would not establish
database freshness, API correctness or Telegram command behavior. No deployment
is authorized or performed by this diagnostic workflow.
