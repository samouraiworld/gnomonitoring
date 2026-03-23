package gnovalidator

import (
	"gorm.io/gorm"
)

// CalculateValidatorRates computes participation rates over the last 10,000 blocks
// for the given chain. Scoping to a recent window avoids full-table scans on chains
// with millions of rows (e.g. test11 with 14.6M rows).
func CalculateValidatorRates(db *gorm.DB, chainID string) ([]ValidatorStat, error) {
	var results []struct {
		Addr               string
		Moniker            string
		TotalBlocks        int64
		ParticipatedBlocks int64
	}

	query := `
		SELECT dp.addr,
		       COALESCE(am.moniker, dp.moniker) AS moniker,
		       COUNT(*) AS total_blocks,
		       SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) AS participated_blocks
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ?
		  AND dp.block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 10000
		GROUP BY dp.addr`

	err := db.Raw(query, chainID, chainID).Scan(&results).Error
	if err != nil {
		return nil, err
	}

	var stats []ValidatorStat
	for _, r := range results {
		rate := 0.0
		if r.TotalBlocks > 0 {
			rate = float64(r.ParticipatedBlocks) / float64(r.TotalBlocks) * 100
		}
		stats = append(stats, ValidatorStat{
			Address: r.Addr,
			Moniker: r.Moniker,
			Rate:    rate,
		})
	}

	return stats, nil
}

// CalculateMissedBlocks counts blocks missed today by each validator.
// Scoped to today's date to avoid scanning the entire history.
func CalculateMissedBlocks(db *gorm.DB, chainID string) ([]MissedBlockStat, error) {
	var results []struct {
		Addr    string
		Moniker string
		Missed  int
	}

	query := `
		SELECT dp.addr,
		       COALESCE(am.moniker, dp.moniker) AS moniker,
		       SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END) AS missed
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ?
		  AND dp.date >= date('now')
		GROUP BY dp.addr`

	err := db.Raw(query, chainID).Scan(&results).Error
	if err != nil {
		return nil, err
	}

	var stats []MissedBlockStat
	for _, r := range results {
		stats = append(stats, MissedBlockStat{
			Address: r.Addr,
			Moniker: r.Moniker,
			Missed:  r.Missed,
		})
	}

	return stats, nil
}

// CalculateConsecutiveMissedBlocks computes the current streak of consecutive
// missed blocks for each validator, looking at the last 200 blocks.
// Uses a two-step approach: first fetch max height (fast indexed lookup),
// then query the narrow range to avoid the correlated subquery overhead.
func CalculateConsecutiveMissedBlocks(db *gorm.DB, chainID string) ([]ConsecutiveMissedStat, error) {
	// Step 1: get max block height for this chain (uses idx_dp_chain_block_height)
	var maxHeight int64
	err := db.Raw(`SELECT COALESCE(MAX(block_height), 0) FROM daily_participations WHERE chain_id = ?`, chainID).Scan(&maxHeight).Error
	if err != nil {
		return nil, err
	}
	if maxHeight == 0 {
		return nil, nil
	}

	// Step 2: fetch only the columns we need for the last 200 blocks
	type row struct {
		Addr         string
		Moniker      string
		Participated bool
	}
	var rows []row

	query := `
		SELECT dp.addr,
		       COALESCE(am.moniker, dp.moniker) AS moniker,
		       dp.participated
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ?
		  AND dp.block_height > ?
		ORDER BY dp.addr, dp.block_height ASC`

	err = db.Raw(query, chainID, maxHeight-200).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	type streak struct {
		moniker string
		count   int
	}
	streaks := make(map[string]streak)

	for _, r := range rows {
		s := streaks[r.Addr]
		s.moniker = r.Moniker

		if r.Participated {
			s.count = 0
		} else {
			s.count++
		}
		streaks[r.Addr] = s
	}

	var results []ConsecutiveMissedStat
	for addr, s := range streaks {
		results = append(results, ConsecutiveMissedStat{
			Address: addr,
			Moniker: s.moniker,
			Count:   s.count,
		})
	}

	return results, nil
}
