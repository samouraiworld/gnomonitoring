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
