package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ValidatorScoreRaw holds the raw per-validator alert metrics for one period.
type ValidatorScoreRaw struct {
	Addr           string `json:"addr"`
	Moniker        string `json:"moniker"`
	CriticalCount  int    `json:"critical_count"`
	WarningCount   int    `json:"warning_count"`
	DowntimeBlocks int64  `json:"downtime_blocks"`
}

// periodBounds returns [start,end) for a report period. Mirrors GetAlertLog.
func periodBounds(period string, now time.Time) (time.Time, time.Time, error) {
	switch period {
	case "last_24h":
		return now.Add(-24 * time.Hour), now, nil
	case "current_week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -weekday+1)
		return start, start.AddDate(0, 0, 7), nil
	case "current_month":
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0), nil
	case "current_year":
		start := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(1, 0, 0), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("invalid period: %s", period)
	}
}

// ValidatorIdentity is a validator's address and resolved moniker.
type ValidatorIdentity struct {
	Addr    string `json:"addr"`
	Moniker string `json:"moniker"`
}

// GetChainValidators returns the distinct set of validators that participated
// on the chain during the current calendar year, with monikers resolved from
// addr_monikers (falling back to the moniker stored on the participation rows).
// It unions the aggregated and raw participation tables so validators whose raw
// rows have been pruned are still included. Scoped to chain_id.
func GetChainValidators(db *gorm.DB, chainID string) ([]ValidatorIdentity, error) {
	yearStart := fmt.Sprintf("%d-01-01", time.Now().Year())

	var rows []ValidatorIdentity
	err := db.Raw(`
		SELECT combined.addr AS addr,
		       COALESCE(MAX(am.moniker), MAX(combined.moniker), '') AS moniker
		FROM (
			SELECT chain_id, addr, moniker
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ?
			UNION ALL
			SELECT chain_id, addr, moniker
			FROM daily_participations
			WHERE chain_id = ? AND date >= ?
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr
		ORDER BY combined.addr
	`, chainID, yearStart, chainID, yearStart).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("GetChainValidators(%s): %w", chainID, err)
	}
	return rows, nil
}

// GetValidatorScores returns per-validator CRITICAL/WARNING counts and summed
// downtime blocks for the given chain and period. Ongoing outages
// (end_height = 0) contribute 0 downtime. Scoped to chain_id.
func GetValidatorScores(db *gorm.DB, chainID, period string) ([]ValidatorScoreRaw, error) {
	start, end, err := periodBounds(period, time.Now())
	if err != nil {
		return nil, err
	}

	var rows []ValidatorScoreRaw
	err = db.Raw(`
		SELECT al.addr AS addr,
		       COALESCE(MAX(am.moniker), MAX(al.moniker), '') AS moniker,
		       COUNT(*) FILTER (WHERE al.level = 'CRITICAL') AS critical_count,
		       COUNT(*) FILTER (WHERE al.level = 'WARNING')  AS warning_count,
		       COALESCE(SUM(
		           CASE WHEN al.level = 'CRITICAL' AND al.end_height > al.start_height
		                THEN al.end_height - al.start_height ELSE 0 END
		       ), 0) AS downtime_blocks
		FROM alert_logs al
		LEFT JOIN addr_monikers am ON am.chain_id = al.chain_id AND am.addr = al.addr
		WHERE al.chain_id = ?
		  AND al.level IN ('CRITICAL','WARNING')
		  AND al.sent_at >= ? AND al.sent_at < ?
		GROUP BY al.addr
		ORDER BY al.addr
	`, chainID, start, end).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("GetValidatorScores(%s,%s): %w", chainID, period, err)
	}
	return rows, nil
}
