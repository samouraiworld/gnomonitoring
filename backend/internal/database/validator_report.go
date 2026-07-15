package database

import (
	"sort"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
	"gorm.io/gorm"
)

// ValidatorReportEntry is one validator's computed score for a single report
// period, already filtered against current valset membership. It is the
// shared data shape behind both the Validator Health Report API
// (internal/api/api_report.go) and the daily report renderers
// (internal/gnovalidator).
type ValidatorReportEntry struct {
	Addr                string
	Moniker             string
	Score               int
	Tier                score.Tier
	SignRate            float64
	ProposerReliability *float64
	VotingPower         int64
	SumVotingPower      int64
	CriticalCount       int
	WarningCount        int
	IncidentCount       int
	IncidentRatePerWeek float64
	DowntimeBlocks      int64
	MissedBlocks        int64
}

// mergedValidatorInputs joins one validator's participation and alert rows
// for a single period, keyed by address.
type mergedValidatorInputs struct {
	in      score.Inputs
	moniker string
}

// BuildChainValidatorReport computes every valset-filtered validator's score
// for one chain and one report period ("last_24h", "current_week",
// "current_month", "current_year"). When addrFilter is non-empty, only that
// address is considered (still subject to the valset filter — a departed
// validator's exact address returns an empty slice, not an error).
//
// Valset-membership filtering: a validator absent from GetValidatorVP's
// result has left the valset and is excluded, UNLESS the chain has never
// captured a single VP snapshot yet (graceful degradation for a
// newly-enabled chain). Importantly, this function intentionally includes
// current valset members even if they have zero participation/alert history
// (e.g., newly-joined validators), scoring them at 0/Critical. This is a
// deliberate design choice (not just a port of GetValidatorReportHandler):
// per the documented scoring principle, a validator with no participation
// data scores 0/Critical, so such validators should be visible in the report
// rather than silently omitted.
func BuildChainValidatorReport(db *gorm.DB, chainID, period, addrFilter string) ([]ValidatorReportEntry, error) {
	cfgRows, err := GetAllAdminConfigs(db)
	if err != nil {
		return nil, err
	}
	cfg := make(map[string]string, len(cfgRows))
	for _, c := range cfgRows {
		cfg[c.Key] = c.Value
	}
	weights := score.WeightsFromConfig(cfg)

	vpByAddr, vpSum, vpMax, err := GetValidatorVP(db, chainID)
	if err != nil {
		return nil, err
	}
	valsetFilterActive := len(vpByAddr) > 0

	roster, err := GetChainValidators(db, chainID)
	if err != nil {
		return nil, err
	}

	byAddr := map[string]*ValidatorReportEntry{}
	order := []string{}
	seed := func(addr, moniker string) *ValidatorReportEntry {
		e, ok := byAddr[addr]
		if !ok {
			e = &ValidatorReportEntry{Addr: addr, Moniker: moniker}
			byAddr[addr] = e
			order = append(order, addr)
		}
		return e
	}
	for _, v := range roster {
		if addrFilter != "" && v.Addr != addrFilter {
			continue
		}
		seed(v.Addr, v.Moniker)
	}
	// Also seed current valset members, even if they have no participation
	// history (e.g., newly-joined validators). This is intentional: validators
	// who just joined the valset should be visible in the report at 0/Critical
	// rather than silently omitted, consistent with the design principle that
	// a validator with no participation data scores 0/Critical.
	if valsetFilterActive {
		vpAddrs := make([]string, 0, len(vpByAddr))
		for addr := range vpByAddr {
			vpAddrs = append(vpAddrs, addr)
		}
		sort.Strings(vpAddrs)
		for _, addr := range vpAddrs {
			if addrFilter != "" && addr != addrFilter {
				continue
			}
			seed(addr, "")
		}
	}

	alertRows, err := GetValidatorScores(db, chainID, period)
	if err != nil {
		return nil, err
	}
	partRows, chainBlocks, err := GetValidatorParticipation(db, chainID, period)
	if err != nil {
		return nil, err
	}
	periodDays, err := PeriodElapsedDays(period, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	merged := map[string]*mergedValidatorInputs{}
	ensure := func(addr string) *mergedValidatorInputs {
		m, ok := merged[addr]
		if !ok {
			m = &mergedValidatorInputs{}
			merged[addr] = m
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

	mergedAddrs := make([]string, 0, len(merged))
	for addr := range merged {
		mergedAddrs = append(mergedAddrs, addr)
	}
	sort.Strings(mergedAddrs)

	for _, addr := range mergedAddrs {
		if valsetFilterActive {
			if _, inValset := vpByAddr[addr]; !inValset {
				continue
			}
		}
		m := merged[addr]
		in := m.in
		in.VotingPower = vpByAddr[addr]
		in.SumVotingPower = vpSum
		in.MaxVotingPower = vpMax
		in.ChainBlocks = chainBlocks
		in.PeriodDays = periodDays

		e := seed(addr, m.moniker)
		if e.Moniker == "" {
			e.Moniker = m.moniker
		}

		res := score.Compute(in, weights)
		e.Score = res.Score
		e.Tier = res.Tier
		e.SignRate = res.SignRate
		e.VotingPower = in.VotingPower
		e.SumVotingPower = in.SumVotingPower
		e.CriticalCount = in.CriticalCount
		e.WarningCount = in.WarningCount
		e.IncidentCount = in.IncidentCount
		e.IncidentRatePerWeek = res.IncidentRatePerWeek
		e.DowntimeBlocks = in.DowntimeBlocks
		e.MissedBlocks = in.TotalBlocks - in.SignedBlocks
		if res.ProposerScored {
			pr := res.ProposerReliability
			e.ProposerReliability = &pr
		}
	}

	emptyRes := score.Compute(score.Inputs{}, weights)

	out := make([]ValidatorReportEntry, 0, len(order))
	for _, addr := range order {
		if valsetFilterActive {
			if _, inValset := vpByAddr[addr]; !inValset {
				continue
			}
		}
		e := byAddr[addr]
		if e.SignRate == 0 && e.Score == 0 && e.Tier == "" {
			// Roster member with no merged data this period: same zero-value
			// score every absent period gets in GetValidatorReportHandler.
			e.Score = emptyRes.Score
			e.Tier = emptyRes.Tier
		}
		out = append(out, *e)
	}
	return out, nil
}
