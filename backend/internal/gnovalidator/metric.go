package gnovalidator

import "database/sql"

func CalculateValidatorRates(db *sql.DB) ([]ValidatorStat, error) {
	query := `
		SELECT 
			addr,
			moniker,
			COUNT(*) AS total_blocks,
			SUM(CASE WHEN participated THEN 1 ELSE 0 END) AS participated_blocks
		FROM daily_participation
		
		GROUP BY addr, moniker
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []ValidatorStat
	for rows.Next() {
		var stat ValidatorStat
		var totalBlocks, participatedBlocks int

		if err := rows.Scan(&stat.Address, &stat.Moniker, &totalBlocks, &participatedBlocks); err != nil {
			return nil, err
		}

		if totalBlocks > 0 {
			stat.Rate = float64(participatedBlocks) / float64(totalBlocks) * 100
		} else {
			stat.Rate = 0
		}

		stats = append(stats, stat)
	}

	return stats, rows.Err()
}
func CalculateMissedBlocks(db *sql.DB) ([]MissedBlockStat, error) {
	query := `
		SELECT addr,
		       moniker,
			   SUM(CASE WHEN participated = false THEN 1 ELSE 0 END) AS missed
		FROM daily_participation
		GROUP BY addr, moniker
	`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []MissedBlockStat
	for rows.Next() {
		var stat MissedBlockStat
		if err := rows.Scan(&stat.Address, &stat.Moniker, &stat.Missed); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, nil
}
func CalculateConsecutiveMissedBlocks(db *sql.DB) ([]ConsecutiveMissedStat, error) {
	query := `
		SELECT addr, moniker, block_height, participated
		FROM daily_participation
		ORDER BY addr, block_height ASC
	`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type streak struct {
		moniker string
		count   int
	}

	streaks := make(map[string]streak)

	for rows.Next() {
		var addr, moniker string
		var height int64
		var participated bool

		if err := rows.Scan(&addr, &moniker, &height, &participated); err != nil {
			return nil, err
		}

		s := streaks[addr]
		s.moniker = moniker

		if participated {
			// Dès qu’il a participé, le streak repart à 0
			s.count = 0
		} else {
			// Il n’a pas participé → streak++
			s.count++
		}

		streaks[addr] = s
	}

	// Transformation finale
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
