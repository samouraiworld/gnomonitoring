package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
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
	// Both must be currently in the valset (voting_power > 0) to appear in the
	// report at all. g1aaa's VP is kept tiny relative to g1healthy's so the
	// severity ramp on g1aaa's penalty rounds away to ~0 (1/1000 share), not
	// perturbing the expected score below.
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1aaa", Moniker: "alpha", VotingPower: 1})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon", VotingPower: 1000})

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
	// The frequency penalty now depends on elapsed days in the current month
	// at test-run time (non-deterministic), so derive the expected value the
	// same way production does instead of hardcoding a day-of-month-dependent
	// number.
	periodDays, err := database.PeriodElapsedDays("current_month", now)
	if err != nil {
		t.Fatalf("PeriodElapsedDays: %v", err)
	}
	weights := score.DefaultWeights()
	incidentRatePerWeek := 1.0 / periodDays * 7
	freqPenalty := incidentRatePerWeek * weights.FreqWeight
	if freqPenalty > float64(weights.FreqCap) {
		freqPenalty = float64(weights.FreqCap)
	}
	wantScore := int(math.Round(100 - float64(weights.CriticalWeight) - freqPenalty))
	if p.Score != wantScore || p.Tier != "Excellent" {
		t.Fatalf("score wrong: got (%d,%s), want (%d,Excellent)", p.Score, p.Tier, wantScore)
	}
	// current_month's elapsed-days figure is computed independently here (from
	// the test's now) and inside the handler (from its own time.Now() at
	// request time), so the two periodDays values differ by the wall-clock
	// gap between test setup and the HTTP round-trip. That gap is immaterial
	// to the ~13-day elapsed window but is enough to break bit-exact float
	// equality, so compare with a tolerance that a dropped/zeroed field (off
	// by the full ~0.53 rate) would still fail.
	wantRate := incidentRatePerWeek
	if diff := p.IncidentRatePerWeek - wantRate; diff > 1e-4 || diff < -1e-4 {
		t.Fatalf("IncidentRatePerWeek = %v, want %v (diff %v)", p.IncidentRatePerWeek, wantRate, diff)
	}
	if p.IncidentCount != 1 {
		t.Fatalf("want incident_count 1, got %d", p.IncidentCount)
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
	// Must be currently in the valset (voting_power > 0) to appear at all.
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1alertonly", Moniker: "alertonly-mon", VotingPower: 1})

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
	// Both must be currently in the valset (voting_power > 0) to appear at all.
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1miss", Moniker: "miss-mon", VotingPower: 1})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1clean", Moniker: "clean-mon", VotingPower: 1})

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

// TestGetValidatorReportHandlerExcludesDepartedValidators covers the new
// valset-membership filter: a validator absent from addr_monikers (or with
// voting_power=0) has left the valset and must be excluded from the report
// entirely, across every period, even when explicitly targeted via ?addr=.
func TestGetValidatorReportHandlerExcludesDepartedValidators(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	// g1departed: has history this year but is no longer in the valset
	// (no addr_monikers row at all — same as "never captured a VP snapshot").
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1departed", Moniker: "departed-mon",
		BlockHeight: 1, Date: now, Participated: true,
	})
	db.Create(&database.AlertLog{
		ChainID: "test12", Addr: "g1departed", Level: "CRITICAL",
		StartHeight: 100, EndHeight: 130, Moniker: "departed-mon", SentAt: now,
	})

	// g1active: currently in the valset (voting_power > 0).
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1active", Moniker: "active-mon",
		BlockHeight: 2, Date: now, Participated: true,
	})
	db.Create(&database.AddrMoniker{
		ChainID: "test12", Addr: "g1active", Moniker: "active-mon", VotingPower: 5,
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

	t.Run("unfiltered report excludes the departed validator", func(t *testing.T) {
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
		for _, rep := range out {
			if rep.Addr == "g1departed" {
				t.Fatalf("departed validator must be excluded from the report, got: %+v", rep)
			}
		}
		found := false
		for _, rep := range out {
			if rep.Addr == "g1active" {
				found = true
			}
		}
		if !found {
			t.Fatalf("active validator must still be present, got: %+v", out)
		}
	})

	t.Run("explicit addr= filter on a departed validator returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/reports/validators?chain=test12&addr=g1departed", nil)
		rec := httptest.NewRecorder()
		GetValidatorReportHandler(rec, req, db)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var out []validatorReport
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
		}
		if len(out) != 0 {
			t.Fatalf("want empty array for a departed validator's exact address, got: %+v", out)
		}
	})
}

// TestGetValidatorReportHandlerNoVPDataYetShowsEveryone covers the documented
// graceful-degradation case (CLAUDE.md: "before the first VP snapshot, vp=0
// -> severity 1 + proposer dropped -> score = 100*sign_rate - alert_penalties"):
// a chain that has never captured a single voting-power snapshot yet (no
// addr_monikers row at all, e.g. right after a chain is enabled, before
// InitMonikerMap's first successful cycle) must show every validator with
// participation/alert history, not exclude everyone. The valset-membership
// filter only kicks in once VP tracking has produced at least one data point
// for the chain — otherwise "no VP captured yet" would be indistinguishable
// from "everyone departed".
func TestGetValidatorReportHandlerNoVPDataYetShowsEveryone(t *testing.T) {
	db := testoutils.NewTestDB(t)
	now := time.Now().UTC()

	// No addr_monikers rows at all for this chain — simulates a chain before
	// its first successful InitMonikerMap cycle.
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1nvp", Moniker: "no-vp-yet",
		BlockHeight: 1, Date: now, Participated: true,
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
	found := false
	for _, rep := range out {
		if rep.Addr == "g1nvp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("validator must be shown when the chain has no VP data at all yet, got: %+v", out)
	}
}

// TestGetValidatorReportHandlerShowsZeroHistoryValsetMember covers the
// behavior change introduced when GetValidatorReportHandler started
// delegating to database.BuildChainValidatorReport: a current valset member
// (voting_power > 0 in addr_monikers) with zero participation/alert history
// for a period must still appear in the report, scored at 0/Critical, rather
// than being silently omitted. This matches the documented scoring principle
// that a validator with no participation rows scores 0/Critical.
func TestGetValidatorReportHandlerShowsZeroHistoryValsetMember(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// g1newjoin: currently in the valset, but has never signed a block or
	// triggered an alert — no DailyParticipation or AlertLog rows at all.
	db.Create(&database.AddrMoniker{
		ChainID: "test12", Addr: "g1newjoin", Moniker: "newjoin-mon", VotingPower: 10,
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

	rep, ok := byAddr["g1newjoin"]
	if !ok {
		t.Fatalf("zero-history valset member g1newjoin must still appear in the report, got: %+v", out)
	}
	p, ok := rep.Periods["last_24h"]
	if !ok {
		t.Fatalf("missing last_24h period for g1newjoin: %+v", rep)
	}
	if p.Score != 0 || p.Tier != "Critical" {
		t.Fatalf("g1newjoin last_24h score wrong: got (%d,%s), want (0,Critical)", p.Score, p.Tier)
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
