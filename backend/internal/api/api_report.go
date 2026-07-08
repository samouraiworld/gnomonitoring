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
