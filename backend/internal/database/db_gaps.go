package database

import (
	"time"

	"gorm.io/gorm"
)

// FindMissingHeights returns every block height in [min, max] that has no
// row at all in daily_participations for chainID, where min is the earliest
// recorded height among rows dated on/after sinceDate (falling back to the
// overall earliest height if none) and max is the overall latest recorded
// height. Bounding the scan to sinceDate keeps it cheap and matches the
// raw-retention window: heights older than that are already aggregated/
// pruned and out of scope for this repair mechanism.
func FindMissingHeights(db *gorm.DB, chainID string, sinceDate time.Time) ([]int64, error) {
	var bounds struct {
		MinH *int64
		MaxH *int64
	}
	err := db.Raw(`
		SELECT
			COALESCE(
				(SELECT MIN(block_height) FROM daily_participations WHERE chain_id = ? AND date >= ?),
				(SELECT MIN(block_height) FROM daily_participations WHERE chain_id = ?)
			) AS min_h,
			(SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) AS max_h
	`, chainID, sinceDate, chainID, chainID).Scan(&bounds).Error
	if err != nil {
		return nil, err
	}
	if bounds.MinH == nil || bounds.MaxH == nil || *bounds.MinH >= *bounds.MaxH {
		return nil, nil
	}

	var missing []int64
	err = db.Raw(`
		SELECT gs
		FROM generate_series(?::bigint, ?::bigint) gs
		WHERE NOT EXISTS (
			SELECT 1 FROM daily_participations dp
			WHERE dp.chain_id = ? AND dp.block_height = gs
		)
		ORDER BY gs
	`, *bounds.MinH, *bounds.MaxH, chainID).Scan(&missing).Error
	if err != nil {
		return nil, err
	}
	return missing, nil
}
