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
