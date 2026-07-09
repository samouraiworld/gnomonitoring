package api

import (
	"encoding/json"
	"net/http"
	"sort"

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
	DowntimeBlocks      int64    `json:"downtime_blocks"`
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

	vpByAddr, vpSum, vpMax, err := database.GetValidatorVP(db, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// addr -> report, preserving discovery order.
	byAddr := map[string]*validatorReport{}
	order := []string{}

	// Seed the full active-validator roster so healthy validators (no alerts)
	// appear with a clean score, not just validators that have alerted.
	roster, err := database.GetChainValidators(db, chainID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, v := range roster {
		if addrFilter != "" && v.Addr != addrFilter {
			continue
		}
		if _, ok := byAddr[v.Addr]; !ok {
			byAddr[v.Addr] = &validatorReport{Addr: v.Addr, Moniker: v.Moniker, Periods: map[string]periodScore{}}
			order = append(order, v.Addr)
		}
	}

	for _, period := range reportPeriods {
		alertRows, err := database.GetValidatorScores(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		partRows, err := database.GetValidatorParticipation(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Merge both sources into one Inputs per addr.
		inputs := map[string]*score.Inputs{}
		monikers := map[string]string{}
		ensure := func(addr string) *score.Inputs {
			in, ok := inputs[addr]
			if !ok {
				in = &score.Inputs{}
				inputs[addr] = in
			}
			return in
		}
		for _, p := range partRows {
			if addrFilter != "" && p.Addr != addrFilter {
				continue
			}
			in := ensure(p.Addr)
			in.SignedBlocks = p.SignedBlocks
			in.TotalBlocks = p.TotalBlocks
			in.ProposedBlocks = p.ProposedBlocks
		}
		for _, a := range alertRows {
			if addrFilter != "" && a.Addr != addrFilter {
				continue
			}
			in := ensure(a.Addr)
			in.CriticalCount = a.CriticalCount
			in.WarningCount = a.WarningCount
			in.DowntimeBlocks = a.DowntimeBlocks
			if a.Moniker != "" {
				monikers[a.Addr] = a.Moniker
			}
		}

		addrs := make([]string, 0, len(inputs))
		for addr := range inputs {
			addrs = append(addrs, addr)
		}
		sort.Strings(addrs)

		for _, addr := range addrs {
			in := inputs[addr]
			in.VotingPower = vpByAddr[addr]
			in.SumVotingPower = vpSum
			in.MaxVotingPower = vpMax
			rep, ok := byAddr[addr]
			if !ok {
				rep = &validatorReport{Addr: addr, Moniker: monikers[addr], Periods: map[string]periodScore{}}
				byAddr[addr] = rep
				order = append(order, addr)
			}
			if rep.Moniker == "" {
				rep.Moniker = monikers[addr]
			}
			res := score.Compute(*in, weights)
			ps := periodScore{
				Score: res.Score, Tier: string(res.Tier),
				SignRate:       res.SignRate,
				VotingPower:    in.VotingPower,
				CriticalCount:  in.CriticalCount,
				WarningCount:   in.WarningCount,
				DowntimeBlocks: in.DowntimeBlocks,
			}
			if res.ProposerScored {
				pr := res.ProposerReliability
				ps.ProposerReliability = &pr
			}
			rep.Periods[period] = ps
		}
	}

	out := make([]validatorReport, 0, len(order))
	for _, addr := range order {
		rep := byAddr[addr]
		// Ensure every period key exists (zero-value clean score) for absent periods.
		for _, period := range reportPeriods {
			if _, ok := rep.Periods[period]; !ok {
				res := score.Compute(score.Inputs{}, weights)
				rep.Periods[period] = periodScore{Score: res.Score, Tier: string(res.Tier)}
			}
		}
		out = append(out, *rep)
	}
	json.NewEncoder(w).Encode(out)
}
