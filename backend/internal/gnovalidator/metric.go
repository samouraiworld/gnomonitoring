package gnovalidator

import (
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

func CalculateValidatorRates(db *gorm.DB) ([]ValidatorStat, error) {
	var results []struct {
		Addr               string
		Moniker            string
		TotalBlocks        int64
		ParticipatedBlocks int64
	}

	err := db.Model(&database.DailyParticipation{}).
		Select("addr, moniker, COUNT(*) as total_blocks, SUM(CASE WHEN participated THEN 1 ELSE 0 END) as participated_blocks").
		Group("addr, moniker").
		Scan(&results).Error
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

func CalculateMissedBlocks(db *gorm.DB) ([]MissedBlockStat, error) {
	var results []struct {
		Addr    string
		Moniker string
		Missed  int
	}

	err := db.Model(&database.DailyParticipation{}).
		Select("addr, moniker, SUM(CASE WHEN participated = false THEN 1 ELSE 0 END) as missed").
		Group("addr, moniker").
		Scan(&results).Error
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

func CalculateConsecutiveMissedBlocks(db *gorm.DB) ([]ConsecutiveMissedStat, error) {
	var rows []database.DailyParticipation

	err := db.Order("addr, block_height ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}

	type streak struct {
		moniker string
		count   int
	}
	streaks := make(map[string]streak)

	for _, row := range rows {
		s := streaks[row.Addr]
		s.moniker = row.Moniker

		if row.Participated {
			s.count = 0
		} else {
			s.count++
		}
		streaks[row.Addr] = s
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
