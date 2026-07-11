package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
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
	// g1aaa also signs blocks (full sign rate) so the alert penalty is the
	// only thing pulling its score down from 100.
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1aaa", Moniker: "alpha",
		BlockHeight: 199, Date: now, Participated: true, TxContribution: true,
	})
	// healthy validator: participates, no alerts.
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon",
		BlockHeight: 200, Date: now, Participated: true, TxContribution: true,
	})

	internal.Config.Chains = map[string]*internal.ChainConfig{
		"test12": {
			RPCEndpoints:     []string{"http://localhost:26657"},
			GraphqlEndpoints: []string{"http://localhost:8080/graphql/query"},
			GnowebEndpoints:  []string{"http://localhost:8080"},
			Enabled:          true,
		},
	}
	internal.EnabledChains = []string{"test12"}
	internal.Config.DefaultChain = "test12"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

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
	if len(out) != 2 {
		t.Fatalf("unexpected payload: %+v", out)
	}
	byAddr := make(map[string]validatorReport, len(out))
	for _, rep := range out {
		byAddr[rep.Addr] = rep
	}

	alerting, ok := byAddr["g1aaa"]
	if !ok {
		t.Fatalf("missing alerting validator g1aaa in payload: %+v", out)
	}
	p := alerting.Periods["current_month"]
	if p.CriticalCount != 1 || p.DowntimeBlocks != 30 {
		t.Fatalf("current_month wrong: %+v", p)
	}
	if p.Score != 94 || p.Tier != "Excellent" {
		t.Fatalf("score wrong: got (%d,%s), want (94,Excellent)", p.Score, p.Tier)
	}
	if _, ok := alerting.Periods["last_24h"]; !ok {
		t.Fatalf("missing last_24h period")
	}

	healthy, ok := byAddr["g1healthy"]
	if !ok {
		t.Fatalf("missing healthy validator g1healthy in payload: %+v", out)
	}
	if healthy.Moniker != "healthy-mon" {
		t.Fatalf("want moniker healthy-mon, got %q", healthy.Moniker)
	}
	hp := healthy.Periods["current_month"]
	if hp.Score != 100 || hp.Tier != "Excellent" {
		t.Fatalf("healthy score wrong: got (%d,%s), want (100,Excellent)", hp.Score, hp.Tier)
	}
	if hp.CriticalCount != 0 || hp.WarningCount != 0 || hp.DowntimeBlocks != 0 {
		t.Fatalf("healthy validator should have clean counts: %+v", hp)
	}
}

// TestGetValidatorReportHandlerAlertOnlyNoParticipation covers the new
// sign-base behavior: a validator with an alert row but no participation
// rows for a period has TotalBlocks == 0, so signRate (and therefore the
// score base) is 0 — the validator scores 0 and lands in tier "Critical"
// for that period, regardless of the alert penalty itself.
func TestGetValidatorReportHandlerAlertOnlyNoParticipation(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()
	db.Create(&database.AlertLog{
		ChainID: "test12", Addr: "g1alertonly", Level: "CRITICAL",
		StartHeight: 100, EndHeight: 130, Moniker: "alertonly-mon", SentAt: now,
	})

	internal.Config.Chains = map[string]*internal.ChainConfig{
		"test12": {
			RPCEndpoints:     []string{"http://localhost:26657"},
			GraphqlEndpoints: []string{"http://localhost:8080/graphql/query"},
			GnowebEndpoints:  []string{"http://localhost:8080"},
			Enabled:          true,
		},
	}
	internal.EnabledChains = []string{"test12"}
	internal.Config.DefaultChain = "test12"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

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
	byAddr := make(map[string]validatorReport, len(out))
	for _, rep := range out {
		byAddr[rep.Addr] = rep
	}

	rep, ok := byAddr["g1alertonly"]
	if !ok {
		t.Fatalf("missing alert-only validator g1alertonly in payload: %+v", out)
	}
	p := rep.Periods["current_month"]
	if p.Score != 0 || p.Tier != "Critical" {
		t.Fatalf("alert-only score wrong: got (%d,%s), want (0,Critical)", p.Score, p.Tier)
	}
	if p.SignRate != 0 {
		t.Fatalf("want sign_rate 0 (no participation rows), got %v", p.SignRate)
	}
}

// TestGetValidatorReportHandlerMissedAndLastAlert covers the two new display
// columns: per-period missed_blocks (= total − signed) and the global,
// period-independent days_since_last_alert (nil when a validator never alerted).
func TestGetValidatorReportHandlerMissedAndLastAlert(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	// g1miss: 3 blocks today, 2 signed / 1 missed; last alert 3 days ago.
	for _, b := range []struct {
		h    int64
		part bool
	}{{1, true}, {2, true}, {3, false}} {
		db.Create(&database.DailyParticipation{
			ChainID: "test12", Addr: "g1miss", Moniker: "miss-mon",
			BlockHeight: b.h, Date: now, Participated: b.part,
		})
	}
	db.Create(&database.AlertLog{
		ChainID: "test12", Addr: "g1miss", Level: "WARNING",
		Moniker: "miss-mon", SentAt: now.Add(-72 * time.Hour),
	})
	// g1clean: participates, never alerted → days_since_last_alert must be null.
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1clean", Moniker: "clean-mon",
		BlockHeight: 4, Date: now, Participated: true,
	})

	internal.Config.Chains = map[string]*internal.ChainConfig{
		"test12": {
			RPCEndpoints:     []string{"http://localhost:26657"},
			GraphqlEndpoints: []string{"http://localhost:8080/graphql/query"},
			GnowebEndpoints:  []string{"http://localhost:8080"},
			Enabled:          true,
		},
	}
	internal.EnabledChains = []string{"test12"}
	internal.Config.DefaultChain = "test12"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

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
	byAddr := make(map[string]validatorReport, len(out))
	for _, rep := range out {
		byAddr[rep.Addr] = rep
	}

	miss, ok := byAddr["g1miss"]
	if !ok {
		t.Fatalf("missing g1miss in payload: %+v", out)
	}
	if miss.DaysSinceLastAlert == nil || *miss.DaysSinceLastAlert != 3 {
		t.Fatalf("g1miss days_since_last_alert = %v, want 3", miss.DaysSinceLastAlert)
	}
	if mp := miss.Periods["current_month"]; mp.MissedBlocks != 1 {
		t.Fatalf("g1miss current_month missed_blocks = %d, want 1", mp.MissedBlocks)
	}

	clean, ok := byAddr["g1clean"]
	if !ok {
		t.Fatalf("missing g1clean in payload: %+v", out)
	}
	if clean.DaysSinceLastAlert != nil {
		t.Fatalf("g1clean days_since_last_alert should be nil, got %v", *clean.DaysSinceLastAlert)
	}
}

func TestGetValidatorReportHandlerInvalidChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	internal.Config.Chains = map[string]*internal.ChainConfig{
		"test12": {
			RPCEndpoints:     []string{"http://localhost:26657"},
			GraphqlEndpoints: []string{"http://localhost:8080/graphql/query"},
			GnowebEndpoints:  []string{"http://localhost:8080"},
			Enabled:          true,
		},
	}
	internal.EnabledChains = []string{"test12"}
	internal.Config.DefaultChain = "test12"
	defer func() {
		internal.Config.Chains = nil
		internal.EnabledChains = []string{}
		internal.Config.DefaultChain = ""
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/reports/validators?chain=doesnotexist", nil)
	rec := httptest.NewRecorder()
	GetValidatorReportHandler(rec, req, db)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}
