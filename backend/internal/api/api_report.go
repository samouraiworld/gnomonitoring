package api

import (
	"encoding/json"
	"net/http"
	"sort"
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

// merged carries one validator's score inputs plus the moniker discovered while
// joining participation and alert rows for a period.
type merged struct {
	in      score.Inputs
	moniker string
}

// mergeParticipationAndAlerts joins per-validator participation and alert rows
// into one map keyed by address, honoring an optional address filter. Moniker is
// taken from alert rows (participation rows carry no moniker here).
func mergeParticipationAndAlerts(partRows []database.ParticipationRaw, alertRows []database.ValidatorScoreRaw, addrFilter string) map[string]*merged {
	out := map[string]*merged{}
	ensure := func(addr string) *merged {
		m, ok := out[addr]
		if !ok {
			m = &merged{}
			out[addr] = m
		}
		return m
	}
	for _, p := range partRows {
		if addrFilter != "" && p.Addr != addrFilter {
			continue
		}
		m := ensure(p.Addr)
		m.in.SignedBlocks = p.SignedBlocks
		m.in.TotalBlocks = p.TotalBlocks
		m.in.ProposedBlocks = p.ProposedBlocks
	}
	for _, a := range alertRows {
		if addrFilter != "" && a.Addr != addrFilter {
			continue
		}
		m := ensure(a.Addr)
		m.in.CriticalCount = a.CriticalCount
		m.in.WarningCount = a.WarningCount
		m.in.IncidentCount = a.IncidentCount
		m.in.DowntimeBlocks = a.DowntimeBlocks
		if a.Moniker != "" {
			m.moniker = a.Moniker
		}
	}
	return out
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
		partRows, chainBlocks, err := database.GetValidatorParticipation(db, chainID, period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		byAddrMerged := mergeParticipationAndAlerts(partRows, alertRows, addrFilter)

		addrs := make([]string, 0, len(byAddrMerged))
		for addr := range byAddrMerged {
			addrs = append(addrs, addr)
		}
		sort.Strings(addrs)

		for _, addr := range addrs {
			m := byAddrMerged[addr]
			in := m.in
			in.VotingPower = vpByAddr[addr]
			in.SumVotingPower = vpSum
			in.MaxVotingPower = vpMax
			in.ChainBlocks = chainBlocks

			rep, ok := byAddr[addr]
			if !ok {
				rep = &validatorReport{Addr: addr, Moniker: m.moniker, Periods: map[string]periodScore{}}
				byAddr[addr] = rep
				order = append(order, addr)
			}
			if rep.Moniker == "" {
				rep.Moniker = m.moniker
			}

			res := score.Compute(in, weights)
			ps := periodScore{
				Score: res.Score, Tier: string(res.Tier),
				SignRate:       res.SignRate,
				VotingPower:    in.VotingPower,
				CriticalCount:  in.CriticalCount,
				WarningCount:   in.WarningCount,
				IncidentCount:  in.IncidentCount,
				DowntimeBlocks: in.DowntimeBlocks,
				MissedBlocks:   in.TotalBlocks - in.SignedBlocks,
			}
			if res.ProposerScored {
				pr := res.ProposerReliability
				ps.ProposerReliability = &pr
			}
			rep.Periods[period] = ps
		}
	}

	emptyRes := score.Compute(score.Inputs{}, weights)
	emptyPeriod := periodScore{Score: emptyRes.Score, Tier: string(emptyRes.Tier)}

	out := make([]validatorReport, 0, len(order))
	for _, addr := range order {
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
