## Description

<!--
What does this PR do, and why is it needed? Prefer the shape:
symptom observed -> root cause -> what changed.
Link the issue it closes: "Closes #123".
-->

## Type of Change

- [ ] feat: New feature
- [ ] fix: Bug fix
- [ ] docs: Documentation
- [ ] refactor: Code restructuring
- [ ] test: Tests
- [ ] chore: Build/CI/tooling

## Checklist

<!-- Check only what you actually ran and verified. Leave the rest unchecked. -->

- [ ] Tests pass — `cd backend && go test -race ./...`
- [ ] Lint is clean — `cd backend && golangci-lint run ./...` and `go vet ./...`
- [ ] Chain-scoping gate passes — `backend/check-chain-scoping.sh`
- [ ] `go.mod` / `go.sum` are tidy — `cd backend && go mod tidy` leaves no diff
- [ ] CHANGELOG.md entry added under `## [Unreleased]`
- [ ] Documentation updated if needed
- [ ] No regressions in existing features

## Database Changes

<!-- Delete this section if the PR touches no model or query. -->

- [ ] New/changed columns are handled by GORM AutoMigrate, or a migration is included
- [ ] Every query against a chain-scoped table (`daily_participations`, `alert_logs`,
      `addr_monikers`, `telegram_validator_subs`, `govdaos`) carries `WHERE chain_id = ?`
- [ ] Deploy-time behaviour considered: what does the first run after this ships do
      with pre-existing rows? (backfills, one-off reconciliation, alert bursts)

## Verification

<!--
How was this verified beyond CI? For monitoring/alerting changes, name the
gnoland-test scenario used (see gnoland-test/Makefile), or say explicitly that
the change was only covered by unit tests.
-->

## Notes for Reviewers

<!-- Deliberate trade-offs, out-of-scope items, follow-ups worth an issue. -->
