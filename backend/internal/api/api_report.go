package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
	"gorm.io/gorm"
)

var reportPeriods = []string{"last_24h", "current_week", "current_month", "current_year"}

type periodScore struct {
	Score               int      `json:"score"`
	Tier                string   `json:"tier"`
	SignRate            float64  `json:"sign_rate"`
	ProposerReliability *float64 `json:"proposer_reliability"`
	VotingPower         int64    `json:"voting_power"`
	CriticalCount       int      `json:"critical_count"`
	WarningCount        int      `json:"warning_count"`
	IncidentCount       int      `json:"incident_count"`
	IncidentRatePerWeek float64  `json:"incident_rate_per_week"`
	DowntimeBlocks      int64    `json:"downtime_blocks"`
	MissedBlocks        int64    `json:"missed_blocks"`
}

type validatorReport struct {
	Addr    string                 `json:"addr"`
	Moniker string                 `json:"moniker"`
	// DaysSinceLastAlert is global (period-independent): full days elapsed since
	// the validator's most recent WARNING/CRITICAL alert, nil when it never
	// alerted.
	DaysSinceLastAlert *int                   `json:"days_since_last_alert"`
	Periods            map[string]periodScore `json:"periods"`
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

	// Loaded once and reused across every period below (see
	// ValidatorReportContext's doc comment): admin-config score weights, the
	// current VP snapshot, and the validator roster don't vary by period, so
	// re-fetching them on each of the 4 BuildChainValidatorReport calls would
	// be 4x the necessary admin_config/addr_monikers/roster round trips.
	//
	// Graceful degradation: if VP tracking has never produced a single data
	// point for this chain (right after it's enabled, before InitMonikerMap's
	// first successful cycle), ctx.ValsetFilterActive is false and the
	// valset-membership filter below is skipped entirely. Otherwise "no VP
	// captured yet" would be indistinguishable from "everyone departed" and
	// hide every validator, contradicting the documented degraded state (vp=0
	// for everyone -> severity 1, proposer dropped, but validators still
	// shown). Once at least one VP snapshot exists for the chain, resume the
	// normal per-addr departure filter.
	ctx, err := database.LoadValidatorReportContext(db, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Global (period-independent) recency signal: most recent alert per addr.
	lastAlertByAddr, err := database.GetLastAlertTimes(db, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()

	// addr -> report, preserving discovery order.
	byAddr := map[string]*validatorReport{}
	order := []string{}

	for _, period := range reportPeriods {
		entries, err := database.BuildChainValidatorReport(db, ctx, chainID, period, addrFilter)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, e := range entries {
			rep, ok := byAddr[e.Addr]
			if !ok {
				rep = &validatorReport{Addr: e.Addr, Moniker: e.Moniker, Periods: map[string]periodScore{}}
				byAddr[e.Addr] = rep
				order = append(order, e.Addr)
			}
			if rep.Moniker == "" {
				rep.Moniker = e.Moniker
			}
			rep.Periods[period] = periodScore{
				Score: e.Score, Tier: string(e.Tier),
				SignRate:            e.SignRate,
				ProposerReliability: e.ProposerReliability,
				VotingPower:         e.VotingPower,
				CriticalCount:       e.CriticalCount,
				WarningCount:        e.WarningCount,
				IncidentCount:       e.IncidentCount,
				IncidentRatePerWeek: e.IncidentRatePerWeek,
				DowntimeBlocks:      e.DowntimeBlocks,
				MissedBlocks:        e.MissedBlocks,
			}
		}
	}

	emptyRes := score.Compute(score.Inputs{}, ctx.Weights)
	emptyPeriod := periodScore{Score: emptyRes.Score, Tier: string(emptyRes.Tier)}

	out := make([]validatorReport, 0, len(order))
	for _, addr := range order {
		// ctx.VPByAddr only contains addrs with voting_power > 0
		// (GetValidatorVP) — a validator absent here has left the valset.
		// Exclude it from the report entirely (every period), even under an
		// explicit ?addr= match. Gated by ctx.ValsetFilterActive: skipped
		// entirely during the graceful-degradation window (see above) so a
		// chain with no VP data yet still shows everyone instead of excluding
		// all of them.
		if ctx.ValsetFilterActive {
			if _, inValset := ctx.VPByAddr[addr]; !inValset {
				continue
			}
		}
		rep := byAddr[addr]
		if t, ok := lastAlertByAddr[addr]; ok {
			d := int(now.Sub(t).Hours() / 24)
			rep.DaysSinceLastAlert = &d
		}
		// Ensure every period key exists (zero-value clean score) for absent periods.
		for _, period := range reportPeriods {
			if _, ok := rep.Periods[period]; !ok {
				rep.Periods[period] = emptyPeriod
			}
		}
		out = append(out, *rep)
	}
	json.NewEncoder(w).Encode(out)
}
