# Validator Health Report Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-chain validator "health score" report, computed from alert history over four time windows, exposed as a JSON API, surfaced in the admin panel, and linked from the daily summary.

**Architecture:** A pure scoring function in a new `score` package turns raw per-validator alert counts into a 0-100 score + tier. A `database` query aggregates `alert_logs` per period. Two `net/http` handlers expose the JSON. The daily-report sender appends a memba link when the chain's toggle is on. The React admin panel gets a Reports page reading the same API.

**Tech Stack:** Go (`net/http`, GORM, PostgreSQL), React + TypeScript + Vite (admin panel).

## Global Constraints

- All code, comments, commit messages, and docs in **English** only.
- Never add a `Co-Authored-By` / Claude attribution line to commits.
- Every query against `alert_logs` must be scoped `WHERE chain_id = ?`.
- Backend commands run from `backend/`. Tests need a reachable Postgres (`testoutils.NewTestDB(t)`); DSN override via `TEST_DATABASE_DSN`.
- Build check: `go build ./...`; vet: `go vet ./...`; tests: `go test ./...`.
- Period identifiers are exactly: `last_24h`, `current_week`, `current_month`, `current_year`.
- Tier values are exactly: `Excellent`, `Good`, `Watch`, `Critical`.
- Scoring defaults (overridable via `admin_config`): `−6` per CRITICAL (cap `−60`); downtime `−1` per `500` blocks (cap `−20`); base `100`.
- `admin_config` keys: `report_score_critical_weight`, `report_score_critical_cap`, `report_score_downtime_blocks_per_point`, `report_score_downtime_cap`, `validator_report_enabled.<chainID>`, `report_base_url`.

---

## Task 1: Pure scoring function (`score` package)

**Files:**
- Create: `backend/internal/score/score.go`
- Test: `backend/internal/score/score_test.go`

**Interfaces:**
- Consumes: nothing (pure, no DB).
- Produces:
  - `type Tier string` with consts `TierExcellent = "Excellent"`, `TierGood = "Good"`, `TierWatch = "Watch"`, `TierCritical = "Critical"`.
  - `type Weights struct { CriticalWeight, CriticalCap, DowntimeBlocksPerPoint, DowntimeCap int }`
  - `func DefaultWeights() Weights`
  - `func Compute(criticalCount int, downtimeBlocks int64, w Weights) (int, Tier)`

- [ ] **Step 1: Write the failing test**

```go
package score

import "testing"

func TestComputeClean(t *testing.T) {
	s, tier := Compute(0, 0, DefaultWeights())
	if s != 100 || tier != TierExcellent {
		t.Fatalf("clean validator: got (%d,%s), want (100,Excellent)", s, tier)
	}
}

func TestComputeCriticalPenalty(t *testing.T) {
	// 2 criticals * -6 = -12 -> 88 -> Good
	s, tier := Compute(2, 0, DefaultWeights())
	if s != 88 || tier != TierGood {
		t.Fatalf("got (%d,%s), want (88,Good)", s, tier)
	}
}

func TestComputeCriticalCap(t *testing.T) {
	// 100 criticals capped at -60 -> 40 -> Watch
	s, tier := Compute(100, 0, DefaultWeights())
	if s != 40 || tier != TierWatch {
		t.Fatalf("got (%d,%s), want (40,Watch)", s, tier)
	}
}

func TestComputeDowntimePenalty(t *testing.T) {
	// 1500 blocks / 500 = -3, no criticals -> 97 -> Excellent
	s, tier := Compute(0, 1500, DefaultWeights())
	if s != 97 || tier != TierExcellent {
		t.Fatalf("got (%d,%s), want (97,Excellent)", s, tier)
	}
}

func TestComputeDowntimeCap(t *testing.T) {
	// 100000 blocks capped at -20 -> 80 -> Good
	s, tier := Compute(0, 100000, DefaultWeights())
	if s != 80 || tier != TierGood {
		t.Fatalf("got (%d,%s), want (80,Good)", s, tier)
	}
}

func TestComputeFloorAndCriticalTier(t *testing.T) {
	// 100 criticals (-60) + huge downtime (-20) = -80 -> 20 -> Critical
	s, tier := Compute(100, 100000, DefaultWeights())
	if s != 20 || tier != TierCritical {
		t.Fatalf("got (%d,%s), want (20,Critical)", s, tier)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/score/ -run TestCompute -v`
Expected: FAIL — package/functions not defined (compile error).

- [ ] **Step 3: Write minimal implementation**

```go
// Package score turns raw per-validator alert metrics into a 0-100 health
// score and a human-readable tier. It is a pure package with no DB dependency
// so the scoring policy can be unit-tested in isolation.
package score

// Tier is the human-readable band a score falls into.
type Tier string

const (
	TierExcellent Tier = "Excellent"
	TierGood      Tier = "Good"
	TierWatch     Tier = "Watch"
	TierCritical  Tier = "Critical"
)

// Weights holds the tunable scoring parameters (loaded from admin_config in
// production, defaulted here). All values are positive magnitudes.
type Weights struct {
	CriticalWeight         int // points lost per CRITICAL alert
	CriticalCap            int // max points lost from criticals
	DowntimeBlocksPerPoint int // blocks of downtime that cost 1 point
	DowntimeCap            int // max points lost from downtime
}

// DefaultWeights returns the starting calibration.
func DefaultWeights() Weights {
	return Weights{
		CriticalWeight:         6,
		CriticalCap:            60,
		DowntimeBlocksPerPoint: 500,
		DowntimeCap:            20,
	}
}

// Compute returns the score (0-100) and its tier for a validator over one
// period. criticalCount is the raw number of CRITICAL alert rows (resends
// included); downtimeBlocks is the summed (end-start) block span of those
// outages.
func Compute(criticalCount int, downtimeBlocks int64, w Weights) (int, Tier) {
	critPenalty := criticalCount * w.CriticalWeight
	if critPenalty > w.CriticalCap {
		critPenalty = w.CriticalCap
	}

	downPenalty := 0
	if w.DowntimeBlocksPerPoint > 0 {
		downPenalty = int(downtimeBlocks / int64(w.DowntimeBlocksPerPoint))
	}
	if downPenalty > w.DowntimeCap {
		downPenalty = w.DowntimeCap
	}

	s := 100 - critPenalty - downPenalty
	if s < 0 {
		s = 0
	}
	return s, tierFor(s)
}

func tierFor(s int) Tier {
	switch {
	case s >= 85:
		return TierExcellent
	case s >= 60:
		return TierGood
	case s >= 30:
		return TierWatch
	default:
		return TierCritical
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/score/ -v`
Expected: PASS (all `TestCompute*`).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/score/
git commit -m "feat(score): add pure validator health scoring function"
```

---

## Task 2: Weights loader from admin_config

**Files:**
- Modify: `backend/internal/score/score.go` (add loader)
- Test: `backend/internal/score/weights_test.go`

**Interfaces:**
- Consumes: `Weights`, `DefaultWeights` from Task 1. A `map[string]string` of admin_config values (matching what `handleGetThresholds` returns).
- Produces: `func WeightsFromConfig(cfg map[string]string) Weights` — parses the four `report_score_*` keys, falling back to `DefaultWeights()` for any missing/invalid value.

- [ ] **Step 1: Write the failing test**

```go
package score

import "testing"

func TestWeightsFromConfigDefaults(t *testing.T) {
	w := WeightsFromConfig(map[string]string{})
	if w != DefaultWeights() {
		t.Fatalf("empty config should yield defaults, got %+v", w)
	}
}

func TestWeightsFromConfigOverrides(t *testing.T) {
	w := WeightsFromConfig(map[string]string{
		"report_score_critical_weight":           "10",
		"report_score_critical_cap":              "50",
		"report_score_downtime_blocks_per_point": "1000",
		"report_score_downtime_cap":              "15",
	})
	want := Weights{CriticalWeight: 10, CriticalCap: 50, DowntimeBlocksPerPoint: 1000, DowntimeCap: 15}
	if w != want {
		t.Fatalf("got %+v, want %+v", w, want)
	}
}

func TestWeightsFromConfigIgnoresInvalid(t *testing.T) {
	w := WeightsFromConfig(map[string]string{"report_score_critical_weight": "abc"})
	if w.CriticalWeight != DefaultWeights().CriticalWeight {
		t.Fatalf("invalid value should fall back to default, got %d", w.CriticalWeight)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/score/ -run TestWeightsFromConfig -v`
Expected: FAIL — `WeightsFromConfig` undefined.

- [ ] **Step 3: Write minimal implementation**

Append to `backend/internal/score/score.go`:

```go
import "strconv"

// admin_config keys for the tunable scoring weights.
const (
	KeyCriticalWeight         = "report_score_critical_weight"
	KeyCriticalCap            = "report_score_critical_cap"
	KeyDowntimeBlocksPerPoint = "report_score_downtime_blocks_per_point"
	KeyDowntimeCap            = "report_score_downtime_cap"
)

// WeightsFromConfig builds Weights from an admin_config key/value map, using
// DefaultWeights() for any missing or non-integer value.
func WeightsFromConfig(cfg map[string]string) Weights {
	w := DefaultWeights()
	w.CriticalWeight = intOr(cfg, KeyCriticalWeight, w.CriticalWeight)
	w.CriticalCap = intOr(cfg, KeyCriticalCap, w.CriticalCap)
	w.DowntimeBlocksPerPoint = intOr(cfg, KeyDowntimeBlocksPerPoint, w.DowntimeBlocksPerPoint)
	w.DowntimeCap = intOr(cfg, KeyDowntimeCap, w.DowntimeCap)
	return w
}

func intOr(cfg map[string]string, key string, fallback int) int {
	v, ok := cfg[key]
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
```

Note: put the `import "strconv"` into the existing import block at the top of `score.go` rather than a second import statement.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/score/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/score/
git commit -m "feat(score): load scoring weights from admin_config map"
```

---

## Task 3: Period-scoped alert aggregation query

**Files:**
- Create: `backend/internal/database/db_score.go`
- Test: `backend/internal/database/db_score_test.go`

**Interfaces:**
- Consumes: existing `AlertLog` model and `NewTestDB(t)` from `testoutils`. The period-bound logic mirrors `GetAlertLog` (`current_week`/`current_month`/`current_year`) plus `last_24h` from `GetAlertLogsLast24h`.
- Produces:
  - `type ValidatorScoreRaw struct { Addr, Moniker string; CriticalCount, WarningCount int; DowntimeBlocks int64 }`
  - `func periodBounds(period string, now time.Time) (start, end time.Time, err error)` (unexported helper)
  - `func GetValidatorScores(db *gorm.DB, chainID, period string) ([]ValidatorScoreRaw, error)`

**Downtime note:** an ongoing CRITICAL row may have `end_height = 0`; treat `downtime` for such a row as `0` in v1 (avoids needing the live chain height inside a SQL aggregate). Cumulative downtime therefore counts only resolved spans. This is an accepted v1 simplification.

- [ ] **Step 1: Write the failing test**

```go
package database

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func seedAlert(t *testing.T, db *gorm.DB, chain, addr, level string, start, end int64, sentAt time.Time) {
	t.Helper()
	row := AlertLog{
		ChainID: chain, Addr: addr, Level: level,
		StartHeight: start, EndHeight: end, Moniker: addr + "-mon",
		SentAt: sentAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed alert: %v", err)
	}
}

func TestGetValidatorScoresCurrentMonth(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	inMonth := time.Date(now.Year(), now.Month(), 2, 12, 0, 0, 0, time.UTC)

	seedAlert(t, db, "test12", "g1aaa", "CRITICAL", 100, 130, inMonth)
	seedAlert(t, db, "test12", "g1aaa", "WARNING", 90, 95, inMonth)
	// other chain must not leak in
	seedAlert(t, db, "other", "g1aaa", "CRITICAL", 1, 999, inMonth)

	rows, err := GetValidatorScores(db, "test12", "current_month")
	if err != nil {
		t.Fatalf("GetValidatorScores: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 validator, got %d", len(rows))
	}
	got := rows[0]
	if got.Addr != "g1aaa" || got.CriticalCount != 1 || got.WarningCount != 1 || got.DowntimeBlocks != 30 {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.Moniker != "g1aaa-mon" {
		t.Fatalf("want moniker g1aaa-mon, got %q", got.Moniker)
	}
}

func TestGetValidatorScoresInvalidPeriod(t *testing.T) {
	db := testoutils.NewTestDB(t)
	if _, err := GetValidatorScores(db, "test12", "nope"); err == nil {
		t.Fatal("expected error for invalid period")
	}
}
```

(Add `"gorm.io/gorm"` to the test imports.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/database/ -run TestGetValidatorScores -v`
Expected: FAIL — `GetValidatorScores` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ValidatorScoreRaw holds the raw per-validator alert metrics for one period.
type ValidatorScoreRaw struct {
	Addr           string `json:"addr"`
	Moniker        string `json:"moniker"`
	CriticalCount  int    `json:"critical_count"`
	WarningCount   int    `json:"warning_count"`
	DowntimeBlocks int64  `json:"downtime_blocks"`
}

// periodBounds returns [start,end) for a report period. Mirrors GetAlertLog.
func periodBounds(period string, now time.Time) (time.Time, time.Time, error) {
	switch period {
	case "last_24h":
		return now.Add(-24 * time.Hour), now, nil
	case "current_week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -weekday+1)
		return start, start.AddDate(0, 0, 7), nil
	case "current_month":
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0), nil
	case "current_year":
		start := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(1, 0, 0), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("invalid period: %s", period)
	}
}

// GetValidatorScores returns per-validator CRITICAL/WARNING counts and summed
// downtime blocks for the given chain and period. Ongoing outages
// (end_height = 0) contribute 0 downtime. Scoped to chain_id.
func GetValidatorScores(db *gorm.DB, chainID, period string) ([]ValidatorScoreRaw, error) {
	start, end, err := periodBounds(period, time.Now())
	if err != nil {
		return nil, err
	}

	var rows []ValidatorScoreRaw
	err = db.Raw(`
		SELECT al.addr AS addr,
		       COALESCE(MAX(am.moniker), MAX(al.moniker), '') AS moniker,
		       COUNT(*) FILTER (WHERE al.level = 'CRITICAL') AS critical_count,
		       COUNT(*) FILTER (WHERE al.level = 'WARNING')  AS warning_count,
		       COALESCE(SUM(
		           CASE WHEN al.level = 'CRITICAL' AND al.end_height > al.start_height
		                THEN al.end_height - al.start_height ELSE 0 END
		       ), 0) AS downtime_blocks
		FROM alert_logs al
		LEFT JOIN addr_monikers am ON am.chain_id = al.chain_id AND am.addr = al.addr
		WHERE al.chain_id = ?
		  AND al.level IN ('CRITICAL','WARNING')
		  AND al.sent_at >= ? AND al.sent_at < ?
		GROUP BY al.addr
		ORDER BY al.addr
	`, chainID, start, end).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("GetValidatorScores(%s,%s): %w", chainID, period, err)
	}
	return rows, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/database/ -run TestGetValidatorScores -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/database/db_score.go backend/internal/database/db_score_test.go
git commit -m "feat(database): add per-period validator alert aggregation query"
```

---

## Task 4: Report API handler + routes

**Files:**
- Create: `backend/internal/api/api_report.go`
- Modify: `backend/internal/api/api.go` (register route in `StartWebhookAPI`, near the other `mux.HandleFunc` metric routes ~line 1359)
- Test: `backend/internal/api/api_report_test.go`

**Interfaces:**
- Consumes: `database.GetValidatorScores`, `database.ValidatorScoreRaw`, `database.GetAllAdminConfigs` (Task 3 + existing); `score.Compute`, `score.WeightsFromConfig`, `score.Tier` (Tasks 1-2); existing `GetChainIDFromRequest`, `EnableCORS`.
- Produces: `func GetValidatorReportHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB)` serving `GET /api/reports/validators?chain=X[&addr=Z]`.

Response types (declare in `api_report.go`):

```go
type periodScore struct {
	Score          int    `json:"score"`
	Tier           string `json:"tier"`
	CriticalCount  int    `json:"critical_count"`
	WarningCount   int    `json:"warning_count"`
	DowntimeBlocks int64  `json:"downtime_blocks"`
}

type validatorReport struct {
	Addr    string                 `json:"addr"`
	Moniker string                 `json:"moniker"`
	Periods map[string]periodScore `json:"periods"`
}
```

- [ ] **Step 1: Write the failing test**

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestGetValidatorReportHandler(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	db.Create(&database.AlertLog{
		ChainID: "test12", Addr: "g1aaa", Level: "CRITICAL",
		StartHeight: 100, EndHeight: 130, Moniker: "alpha", SentAt: now,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/reports/validators?chain=test12", nil)
	rec := httptest.NewRecorder()
	GetValidatorReportHandler(rec, req, db)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out []validatorReport
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(out) != 1 || out[0].Addr != "g1aaa" {
		t.Fatalf("unexpected payload: %+v", out)
	}
	p := out[0].Periods["current_month"]
	if p.CriticalCount != 1 || p.DowntimeBlocks != 30 {
		t.Fatalf("current_month wrong: %+v", p)
	}
	if p.Score != 94 || p.Tier != "Excellent" {
		t.Fatalf("score wrong: got (%d,%s), want (94,Excellent)", p.Score, p.Tier)
	}
	if _, ok := out[0].Periods["last_24h"]; !ok {
		t.Fatalf("missing last_24h period")
	}
}

func TestGetValidatorReportHandlerInvalidChain(t *testing.T) {
	db := testoutils.NewTestDB(t)
	req := httptest.NewRequest(http.MethodGet, "/api/reports/validators?chain=doesnotexist", nil)
	rec := httptest.NewRecorder()
	GetValidatorReportHandler(rec, req, db)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}
```

Note: score 94 = 100 − (1×6) − (30/500=0). `GetChainIDFromRequest` falls back to `internal.Config.DefaultChain` on an empty `?chain=` and only errors on a chain that fails `ValidateChainID`, so the negative test uses an invalid chain (`doesnotexist`). This test requires config to be loaded; if the api test suite has a setup helper that loads a test config, reuse it — otherwise this test may need `internal.Config` populated with at least one enabled chain. Check how existing `api_test.go` tests set up `internal.Config` and follow that pattern.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/api/ -run TestGetValidatorReportHandler -v`
Expected: FAIL — `GetValidatorReportHandler` / `validatorReport` undefined.

- [ ] **Step 3: Write minimal implementation**

`backend/internal/api/api_report.go`:

```go
package api

import (
	"encoding/json"
	"net/http"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
	"gorm.io/gorm"
)

var reportPeriods = []string{"last_24h", "current_week", "current_month", "current_year"}

type periodScore struct {
	Score          int    `json:"score"`
	Tier           string `json:"tier"`
	CriticalCount  int    `json:"critical_count"`
	WarningCount   int    `json:"warning_count"`
	DowntimeBlocks int64  `json:"downtime_blocks"`
}

type validatorReport struct {
	Addr    string                 `json:"addr"`
	Moniker string                 `json:"moniker"`
	Periods map[string]periodScore `json:"periods"`
}

// GetValidatorReportHandler serves GET /api/reports/validators?chain=X[&addr=Z].
// It is always available regardless of the per-chain report toggle.
func GetValidatorReportHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addrFilter := r.URL.Query().Get("addr")

	cfgRows, err := database.GetAllAdminConfigs(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cfg := make(map[string]string, len(cfgRows))
	for _, c := range cfgRows {
		cfg[c.Key] = c.Value
	}
	weights := score.WeightsFromConfig(cfg)

	// addr -> report, preserving discovery order.
	byAddr := map[string]*validatorReport{}
	order := []string{}

	for _, period := range reportPeriods {
		rows, err := database.GetValidatorScores(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, raw := range rows {
			if addrFilter != "" && raw.Addr != addrFilter {
				continue
			}
			rep, ok := byAddr[raw.Addr]
			if !ok {
				rep = &validatorReport{Addr: raw.Addr, Moniker: raw.Moniker, Periods: map[string]periodScore{}}
				byAddr[raw.Addr] = rep
				order = append(order, raw.Addr)
			}
			if rep.Moniker == "" {
				rep.Moniker = raw.Moniker
			}
			s, tier := score.Compute(raw.CriticalCount, raw.DowntimeBlocks, weights)
			rep.Periods[period] = periodScore{
				Score: s, Tier: string(tier),
				CriticalCount: raw.CriticalCount, WarningCount: raw.WarningCount,
				DowntimeBlocks: raw.DowntimeBlocks,
			}
		}
	}

	out := make([]validatorReport, 0, len(order))
	for _, addr := range order {
		rep := byAddr[addr]
		// Ensure every period key exists (zero-value clean score) for absent periods.
		for _, period := range reportPeriods {
			if _, ok := rep.Periods[period]; !ok {
				s, tier := score.Compute(0, 0, weights)
				rep.Periods[period] = periodScore{Score: s, Tier: string(tier)}
			}
		}
		out = append(out, *rep)
	}
	json.NewEncoder(w).Encode(out)
}
```

Register the route in `api.go` inside `StartWebhookAPI` next to the other metric handlers:

```go
	mux.HandleFunc("/api/reports/validators", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			GetValidatorReportHandler(w, r, db)
		case http.MethodOptions:
			EnableCORS(w, r)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/api/ -run TestGetValidatorReportHandler -v && go build ./...`
Expected: PASS + clean build. If `GetChainIDFromRequest` supplies a default chain (no 400 on missing `?chain=`), change the second test to use `?chain=doesnotexist`.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/api_report.go backend/internal/api/api.go backend/internal/api/api_report_test.go
git commit -m "feat(api): add /api/reports/validators health report endpoint"
```

---

## Task 5: Daily-report link injection

**Files:**
- Modify: `backend/internal/gnovalidator/gnovalidator_report.go` (`SendDailyStatsForUser`, ~line 91-124)
- Test: `backend/internal/gnovalidator/gnovalidator_report_test.go` (add case)

**Interfaces:**
- Consumes: `database.GetAdminConfig` (existing). Reads `validator_report_enabled.<chainID>` and `report_base_url`.
- Produces: `func reportLinkLine(db *gorm.DB, chainID string) string` — returns `"\n📊 Validator report: <base>/reports/<chainID>"` when enabled and base URL set, else `""`.

- [ ] **Step 1: Write the failing test**

```go
func TestReportLinkLine(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Disabled by default -> empty.
	if got := reportLinkLine(db, "test12"); got != "" {
		t.Fatalf("disabled should be empty, got %q", got)
	}

	database.SetAdminConfig(db, "validator_report_enabled.test12", "true")
	database.SetAdminConfig(db, "report_base_url", "https://memba.example")

	got := reportLinkLine(db, "test12")
	want := "\n📊 Validator report: https://memba.example/reports/test12"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Enabled but no base URL -> empty.
	database.SetAdminConfig(db, "validator_report_enabled.test12", "true")
	db.Exec("DELETE FROM admin_configs WHERE key = 'report_base_url'")
	if got := reportLinkLine(db, "test12"); got != "" {
		t.Fatalf("no base url should be empty, got %q", got)
	}
}
```

Ensure the test file imports `database` and `testoutils`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/gnovalidator/ -run TestReportLinkLine -v`
Expected: FAIL — `reportLinkLine` undefined.

- [ ] **Step 3: Write minimal implementation**

Add to `gnovalidator_report.go`:

```go
import "strings"

// reportLinkLine returns the daily-report link line for a chain when the
// per-chain report toggle is on and a base URL is configured, else "".
func reportLinkLine(db *gorm.DB, chainID string) string {
	enabled, err := database.GetAdminConfig(db, "validator_report_enabled."+chainID)
	if err != nil || strings.ToLower(strings.TrimSpace(enabled)) != "true" {
		return ""
	}
	base, err := database.GetAdminConfig(db, "report_base_url")
	if err != nil {
		return ""
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("\n📊 Validator report: %s/reports/%s", base, chainID)
}
```

(`fmt` and `strings` are already imported in this file; add `strings` only if the build complains it is missing.)

Then in `SendDailyStatsForUser`, after the `switch` that builds `msg` (right before the dispatch `switch`), append:

```go
	msg += reportLinkLine(db, chainID)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/gnovalidator/ -run TestReportLinkLine -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/gnovalidator/gnovalidator_report.go backend/internal/gnovalidator/gnovalidator_report_test.go
git commit -m "feat(report): append validator report link to daily summary when enabled"
```

---

## Task 6: memba API documentation

**Files:**
- Create: `docs/validator-report-api.md`

**Interfaces:** none (documentation).

- [ ] **Step 1: Write the doc**

Create `docs/validator-report-api.md` with:
- Endpoint: `GET /api/reports/validators`
- Query params: `chain` (required, must be an enabled chain ID), `addr` (optional, filter to one validator).
- The four period keys and their exact bounds (`last_24h` rolling 24h; `current_week` Monday-based local; `current_month` UTC; `current_year` UTC).
- The response schema (copy the JSON example from the design spec, updated to the fields `score`, `tier`, `critical_count`, `warning_count`, `downtime_blocks`).
- Tier bands: `Excellent ≥85`, `Good ≥60`, `Watch ≥30`, `Critical <30`.
- Scoring summary: base 100; −6 per CRITICAL (cap −60); −1 per 500 downtime blocks (cap −20); weights tunable via admin config.
- A note that the endpoint is always available regardless of the per-chain toggle, and that the toggle only controls whether the daily summary includes the link.
- One concrete `curl` example and one sample JSON response.

- [ ] **Step 2: Verify it renders / no broken references**

Run: `cd /home/louis/Documents/Samourai/repos/gnomonitoring && grep -n "reports/validators" docs/validator-report-api.md`
Expected: at least one match; skim the file for accuracy against `api_report.go`.

- [ ] **Step 3: Commit**

```bash
git add docs/validator-report-api.md
git commit -m "docs: document validator report JSON API for memba integration"
```

---

## Task 7: Panel — API client + types

**Files:**
- Create: `panel/src/types/report.ts`
- Modify: `panel/src/lib/api.ts` is generic already (no change needed unless a helper is wanted)

**Interfaces:**
- Produces TypeScript types mirroring the API response, used by Task 8.

- [ ] **Step 1: Write the types**

`panel/src/types/report.ts`:

```ts
export interface PeriodScore {
  score: number
  tier: 'Excellent' | 'Good' | 'Watch' | 'Critical'
  critical_count: number
  warning_count: number
  downtime_blocks: number
}

export const REPORT_PERIODS = ['last_24h', 'current_week', 'current_month', 'current_year'] as const
export type ReportPeriod = (typeof REPORT_PERIODS)[number]

export interface ValidatorReport {
  addr: string
  moniker: string
  periods: Record<ReportPeriod, PeriodScore>
}
```

- [ ] **Step 2: Typecheck**

Run: `cd panel && npx tsc --noEmit`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add panel/src/types/report.ts
git commit -m "feat(panel): add validator report response types"
```

---

## Task 8: Panel — Reports page + toggle + nav

**Files:**
- Create: `panel/src/pages/Reports.tsx`
- Modify: `panel/src/components/Sidebar.tsx` (add nav item under Monitoring)
- Modify: `panel/src/App.tsx` (add route `reports` to both `ProtectedRoutes` and `DevRoutes`, and the import)

**Interfaces:**
- Consumes: `api.get<ValidatorReport[]>('/api/reports/validators?chain=X')`, `api.get<Record<string,string>>('/admin/config/thresholds')`, `api.put('/admin/config/thresholds', { 'validator_report_enabled.X': 'true' })`, and `api.get` for the chain list (reuse whatever `Chains.tsx`/existing pages use — check `/info` or an existing chains hook).
- Produces: a route at `/reports`.

- [ ] **Step 1: Add the nav item**

In `panel/src/components/Sidebar.tsx`, inside `NAV_ITEMS`, after the monikers entry:

```ts
  { to: '/reports', icon: '📊', label: 'Reports' },
```

- [ ] **Step 2: Add the route**

In `panel/src/App.tsx`: add `import Reports from './pages/Reports'` with the other page imports, and add `<Route path="reports" element={<Reports />} />` inside BOTH `ProtectedRoutes` and `DevRoutes` (next to the `monikers` route).

- [ ] **Step 3: Write the page**

`panel/src/pages/Reports.tsx` — a page that:
- Loads the enabled chains the same way an existing page does (inspect `Chains.tsx` / `AlertHistory.tsx` for the pattern; reuse it — do not invent a new fetch).
- Has a chain `<select>`; on change, calls `api.get<ValidatorReport[]>('/api/reports/validators?chain=' + chain)`.
- Renders a table: columns = Moniker, Address, then per period a group of (Score, Tier, Crit, Warn, Blocks). Start with a single active-period selector (`REPORT_PERIODS`) to keep the table readable; show that period's `PeriodScore` fields per validator row.
- Has an optional address text filter input.
- Reads `/admin/config/thresholds`, derives `validator_report_enabled.<chain>`, renders a toggle; on change PUTs `{ ['validator_report_enabled.'+chain]: next ? 'true' : 'false' }` to `/admin/config/thresholds` and refetches.
- Follows the existing page styling conventions (reuse classes seen in `AlertHistory.tsx`: `form-input`, `btn`, table markup).

Keep it consistent with existing pages; do not add a new design system.

- [ ] **Step 4: Typecheck + build**

Run: `cd panel && npx tsc --noEmit && npm run build`
Expected: no type errors, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add panel/src/pages/Reports.tsx panel/src/components/Sidebar.tsx panel/src/App.tsx
git commit -m "feat(panel): add Reports page with per-chain toggle and score table"
```

---

## Task 9: config.yaml.template + CLAUDE.md notes

**Files:**
- Modify: `backend/config.yaml.template` (document `report_base_url` and the toggle live in admin_config, not YAML — add a comment block)
- Modify: `CLAUDE.md` (add a short "Validator Health Report" subsection describing the endpoint, admin_config keys, and the daily-report link toggle)

**Interfaces:** none.

- [ ] **Step 1: Document in config template**

Add a comment block to `backend/config.yaml.template` near the top explaining that the validator report is toggled per chain live via the admin panel (`admin_config` key `validator_report_enabled.<chainID>`) and that `report_base_url` (also an `admin_config` key) is the memba base used to build the daily-summary link. No YAML keys needed.

- [ ] **Step 2: Document in CLAUDE.md**

Add a concise subsection under a suitable heading summarizing: the `/api/reports/validators` endpoint (always available), the scoring model (critical-driven + capped downtime, weights in admin_config), the four periods, and the per-chain daily-link toggle.

- [ ] **Step 3: Commit**

```bash
git add backend/config.yaml.template CLAUDE.md
git commit -m "docs: document validator report config keys and endpoint in template and CLAUDE.md"
```

---

## Task 10: Full verification

- [ ] **Step 1: Backend build + vet + tests**

Run: `cd backend && go build ./... && go vet ./... && go test ./...`
Expected: all pass (Postgres reachable for tests).

- [ ] **Step 2: Panel typecheck + build**

Run: `cd panel && npx tsc --noEmit && npm run build`
Expected: pass.

- [ ] **Step 3: Manual smoke of the endpoint**

Run the backend locally and:
`curl 'http://localhost:8989/api/reports/validators?chain=test12' | head`
Expected: JSON array (possibly empty) with `periods` objects; HTTP 200.

- [ ] **Step 4: Commit any fixups**

```bash
git add -A && git commit -m "chore(validator-report): verification fixups"
```

---

## Self-Review notes

- Spec coverage: scoring model (T1-2), period query (T3), API always-available (T4), daily link toggle (T5), memba doc (T6), panel page + toggle + metric breakdown (T7-8), config docs (T9). Participation-rate deliberately deferred (spec "out of scope" v1).
- Type consistency: `ValidatorScoreRaw` fields (T3) match the SQL aliases and the handler's `periodScore` mapping (T4) and the TS `PeriodScore` (T7). Period identifiers and tier strings match the Global Constraints everywhere.
- Known assumption to verify at execution: whether `GetChainIDFromRequest` returns a 400 on missing `?chain=` or falls back to a default — Task 4 Step 4 says to adjust the negative test accordingly.
