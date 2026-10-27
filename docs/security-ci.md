# Security checks and publication policy

The `Security` workflow runs on every pull request, on main/develop pushes, on merge groups and on manual requests. It is also called by the image-publication workflow. It uses a read-only repository token and does not deploy, connect to production or send bot messages.

## Blocking policy

| Surface | Blocking check |
| --- | --- |
| Backend and block exporter | `govulncheck` v1.3.0: affected imported packages and called symbols, including test dependencies, with Go 1.26.10 |
| Panel complete dependency tree | npm audit: HIGH/CRITICAL, including dependencies used to build the browser artifact |
| Backend and panel runtime images | Trivy v0.76.1: HIGH/CRITICAL OS and library vulnerabilities, including findings without a fix |
| Scanner infrastructure | Installation, database, network, malformed audit output and scanner errors fail the check |
| Aggregate | Every prerequisite must succeed; failure, cancellation, skipping or a missing prerequisite blocks `Security gate` |

There is no advisory allowlist or `ignore-unfixed` exemption. The Trivy command uses an empty config/ignore file so a repository-local implicit ignore file cannot suppress a finding. Each action is pinned to its reviewed commit; scanner versions are explicit. Updating these pins needs a normal reviewed PR.

The Go gate first prints a module-wide inventory. Findings in a module are not enough to establish that its affected package is built or called; this inventory is informational. Its scanner errors still fail the job. The subsequent package and symbol analyses block findings. In particular, removing an unused import path is not an advisory exception: the module can remain in the inventory while the affected package is absent from the compiled application. Inventory findings need triage and must not be described as a clean dependency graph.

Govulncheck runs in text mode. Its JSON/SARIF/OpenVEX modes can exit zero despite findings and must not replace the blocking invocation. The npm gate validates both the scanner exit status and the complete JSON report; an empty, partial, inconsistent or error response does not count as zero vulnerabilities. Installation and auditing explicitly include development, optional and peer dependencies, so `.npmrc`, `NODE_ENV=production` or an inherited omit setting cannot hide the build dependency tree.

The block exporter is covered by Go source analysis. Its existing Dockerfile is a builder/dev environment, not a published runtime image, and is not represented as a production-image scan. The inspected production project did not run this exporter. Runtime image checks cover backend and panel images built from the candidate source for Linux amd64; they do not attest to the images currently on a server.

## Publication ordering

The publication workflow first requires the reusable security workflow. It then builds the backend locally, scans that exact local image ID again, and only then authenticates to GHCR. The publisher tags and pushes the scanned image ID without rebuilding. A scanner error or policy violation prevents publication. This workflow publishes images; the separate production-deployment workflow is unchanged and was not executed for this security work.

## Required merge check

A failing workflow blocks its own dependent jobs, but merge enforcement also requires a repository rule. As observed on 2026-10-27, ruleset `restrict` (8608571) applies PR/deletion/non-fast-forward rules to every branch and has no required status check.

The additive configuration in `.github/security-required-checks.json` proposes a separate default-branch rule requiring `Security gate` from GitHub Actions (integration ID 15368), with an up-to-date base and no bypass actors. It leaves the existing all-branch ruleset unchanged. The lead must verify the exact successful check context on the final candidate and read back the installed ruleset before claiming merge enforcement is active. This file alone does not change GitHub settings.

PRs without the new check remain blocked once the rule is enabled. They need the security workflow and compatible dependency fixes in their tested tree. Do not replace the required check with an empty success job, a skip, an advisory baseline or an administrator bypass to merge them.

## Local validation

```sh
node --test .github/scripts/security-policy.test.mjs
bash .github/scripts/security-go.sh backend
bash .github/scripts/security-go.sh block_exporter
bash .github/scripts/security-panel.sh
```

Install the pinned govulncheck with Go 1.26.10 before running the Go commands. The npm command needs the panel lockfile to have been installed; CI uses `npm ci --ignore-scripts --include=dev --include=optional --include=peer`. Image checks require the built runtime images and the pinned Trivy scanner. A dated successful run is not a guarantee against advisories disclosed later.

The policy tests exercise audit findings/API errors/malformed responses, Go scanner exit propagation, prerequisite cancellation/skipping, immutable action references and the order of scan/login/publish.

An isolated real npm fixture also verifies the omission regression (2026-10-27, Node 22.22.2/npm 10.9.7): a dev-only `minimist@1.2.0`, project `.npmrc` omitting dev/optional/peer, `NODE_ENV=production` and `NPM_CONFIG_OMIT=dev` make the old audit invocation return exit 0 with zero findings. The corrected audit returns exit 1 with one critical finding. The corrected `npm ci` includes the dev package despite those settings. No vulnerable fixture manifest is committed. A symlink invocation of the audit CLI is covered too: it must still reject malformed JSON. Nine policy tests pass locally; hosted validation uses the pinned Node 22.23.4.

References: [Go vulnerability management](https://go.dev/doc/security/vuln/), [govulncheck exit semantics](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck), [Trivy exit-code policy](https://www.trivy.dev/docs/dev/guide/configuration/others/), [GitHub required status checks](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets).
