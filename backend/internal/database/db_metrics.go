package database

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ====================================== ALERT LOG ======================================
func InsertAlertlog(db *gorm.DB, chainID, addr, moniker, level string, startheight, endheight int64, skipped bool, sent time.Time, msg string) error {
	alert := AlertLog{
		ChainID:     chainID,
		Addr:        addr,
		Moniker:     moniker,
		Level:       level,
		StartHeight: startheight,
		EndHeight:   endheight,
		Skipped:     skipped,
		Msg:         msg,
		SentAt:      sent,
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&alert).Error
}

func GetAlertLog(db *gorm.DB, chainID, period string) ([]AlertSummary, error) {
	var alerts []AlertSummary

	var start, end time.Time
	now := time.Now()

	switch period {
	case "current_week":
		today := time.Now()
		weekday := int(today.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday => 7
		}
		start = today.AddDate(0, 0, -weekday+1) // Return to last Monday
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
		end = start.AddDate(0, 0, 7)
	case "current_month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)

	case "current_year":
		start = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(1, 0, 0)

	case "all_time":
		// Scan the timestamptz bounds directly into time.Time. The Postgres
		// driver returns native timestamps, so no string layout parsing is
		// needed (and string parsing would break on RFC3339 output).
		var minT, maxT sql.NullTime
		if err := db.Raw(`
				SELECT
					MIN(sent_at),
					MAX(sent_at)
				FROM alert_logs
				WHERE chain_id = ?
				`, chainID).Row().Scan(&minT, &maxT); err != nil {
			return nil, fmt.Errorf("error scanning alert log bounds: %w", err)
		}

		if !minT.Valid || !maxT.Valid {
			// No alert_logs rows for this chain: return an empty result.
			return alerts, nil
		}
		start = minT.Time
		end = maxT.Time.AddDate(1, 0, 0)

	default:
		return nil, fmt.Errorf("invalid period: %s", period)
	}

	// Resolve the moniker from addr_monikers (kept current) and fall back to the
	// value frozen into alert_logs when the alert fired, mirroring the other
	// moniker-bearing queries. sent_at is a timestamptz column, so pass the
	// time.Time bounds directly (the pgx driver handles the conversion) rather
	// than formatting to strings.
	err := db.Raw(`
		SELECT DISTINCT
		       COALESCE(NULLIF(am.moniker, 'unknown'), al.moniker, '') AS moniker,
		       al.level, al.addr, al.start_height, al.end_height, al.msg, al.sent_at
		FROM alert_logs al
		LEFT JOIN addr_monikers am ON am.chain_id = al.chain_id AND am.addr = al.addr
		WHERE al.chain_id = ? AND al.sent_at BETWEEN ? AND ?
		ORDER BY al.end_height DESC
		LIMIT 10
	`, chainID, start, end).Scan(&alerts).Error

	return alerts, err
}

// MissedWindow describes one contiguous run of missed blocks for a validator,
// used by the alert detection loop.
type MissedWindow struct {
	Addr        string
	Moniker     string
	StartHeight int64
	EndHeight   int64
	Missed      int
}

// GetMissedWindows returns each contiguous missed-block sequence (>= threshold
// misses) for the given chain within the last 30 minutes. The moniker is
// resolved from addr_monikers, falling back to the value frozen into
// daily_participations. Sequences are grouped by addr only — never by moniker —
// so a streak is not fragmented when a validator's moniker is resolved partway
// through it. Sequences already covered by a RESOLVED alert are excluded.
func GetMissedWindows(db *gorm.DB, chainID string, threshold int) ([]MissedWindow, error) {
	var windows []MissedWindow
	err := db.Raw(`
		WITH ranked AS (
			SELECT
				addr,
				moniker,
				block_height,
				participated,
				CASE
					WHEN participated = false
					 AND LAG(participated) OVER (PARTITION BY addr ORDER BY block_height) IS NOT DISTINCT FROM false
					THEN 0 ELSE 1
				END AS new_seq
			FROM daily_participations
			WHERE chain_id = ? AND date >= NOW() - INTERVAL '30 minutes'
		),
		grouped AS (
			SELECT
				addr,
				moniker,
				block_height,
				participated,
				SUM(new_seq) OVER (PARTITION BY addr ORDER BY block_height) AS seq_id
			FROM ranked
		),
		sequences AS (
			SELECT
				addr,
				MAX(moniker)      AS dp_moniker,
				MIN(block_height) AS start_height,
				MAX(block_height) AS end_height,
				COUNT(*)          AS missed
			FROM grouped
			WHERE participated = false
			GROUP BY addr, seq_id
		)
		SELECT s.addr,
		       COALESCE(NULLIF(am.moniker, 'unknown'), s.dp_moniker, '') AS moniker,
		       s.start_height,
		       s.end_height,
		       s.missed
		FROM sequences s
		LEFT JOIN addr_monikers am ON am.chain_id = ? AND am.addr = s.addr
		WHERE s.missed >= ?
		  AND NOT EXISTS (
		      SELECT 1 FROM alert_logs r
		      WHERE r.chain_id   = ?
		        AND r.addr       = s.addr
		        AND r.level      = 'RESOLVED'
		        AND r.end_height >= s.end_height
		  )
		ORDER BY s.addr, s.start_height
	`, chainID, chainID, threshold, chainID).Scan(&windows).Error
	return windows, err
}

// GetAlertLogsLast24h returns all alert_logs rows sent in the last 24 hours
// for the given chain, ordered by sent_at DESC. Includes WARNING, CRITICAL,
// and RESOLVED rows. Limits to 50 rows to cap message size.
func GetAlertLogsLast24h(db *gorm.DB, chainID string) ([]AlertSummary, error) {
	var results []AlertSummary
	err := db.Raw(`
		SELECT moniker, addr, level, start_height, end_height, msg, sent_at
		FROM alert_logs
		WHERE chain_id = ?
		  AND sent_at >= NOW() - INTERVAL '24 hours'
		  AND level IN ('WARNING', 'CRITICAL', 'RESOLVED')
		ORDER BY sent_at DESC
		LIMIT 50
	`, chainID).Scan(&results).Error
	return results, err
}

// aggregatedThrough is the chain's aggregation watermark (see GetAggregatedThrough),
// used to bound the raw-fallback branch's scan — see fallbackRawWindowStart.
// Callers that invoke several of these metric functions for the same chain in
// one pass should look it up once and share it rather than each function
// re-querying it independently.
func GetCurrentPeriodParticipationRate(db *gorm.DB, chainID, period string, aggregatedThrough time.Time) ([]ParticipationRate, error) {
	var results []ParticipationRate
	startStr, _, err := getPeriodParams(period)
	if err != nil {
		log.Printf("[db] invalid period: %v", err)
		return nil, err
	}
	fallbackStart := fallbackRawWindowStart(aggregatedThrough, startStr)

	// Three-way UNION:
	//   1. Agrega — past complete days (fast path, production)
	//   2. Raw fallback — days not yet in agrega (tests + new chains + agrega lag).
	//      Bounded to the chain's aggregation watermark via fallbackRawWindowStart
	//      — see that function's doc comment for why.
	//   3. Raw today — current day always from raw (never aggregated yet)
	// Moniker fallback chain (symmetric with the streak query): live
	// addr_monikers ('unknown' placeholder excluded) → the moniker frozen into
	// the participation rows themselves → the address only as a last resort.
	// The old MAX(COALESCE(am.moniker, addr)) collapsed EVERY name to a bare
	// address whenever addr_monikers was empty/stale for the chain, even though
	// the correct name was sitting in daily_participation(_agrega)s.
	query := `
		SELECT combined.addr,
			MAX(COALESCE(NULLIF(am.moniker, 'unknown'), NULLIF(combined.moniker, ''), combined.addr)) AS moniker,
			ROUND(SUM(combined.participated_count) * 100.0 / NULLIF(SUM(combined.total_blocks), 0), 1) AS participation_rate
		FROM (
			SELECT chain_id, addr, moniker, participated_count, total_blocks
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ? AND block_date::date < CURRENT_DATE
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				MAX(dp.moniker),
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				COUNT(*)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date::date = dp.date::date
			WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < CURRENT_DATE AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				MAX(dp.moniker),
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				COUNT(*)
			FROM daily_participations dp
			WHERE dp.chain_id = ? AND dp.date >= CURRENT_DATE AND dp.date < CURRENT_DATE + INTERVAL '1 day'
			GROUP BY dp.chain_id, dp.addr
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr
		ORDER BY participation_rate ASC`

	err = db.Raw(query, chainID, startStr, chainID, fallbackStart, chainID).Scan(&results).Error

	return results, err
}

// ====================================== Up Time / tx_contrib Metrics ==========================
// aggregatedThrough is the chain's aggregation watermark — see
// GetCurrentPeriodParticipationRate's doc comment.
func OperationTimeMetricsaddr(db *gorm.DB, chainID string, aggregatedThrough time.Time) ([]OperationTimeMetrics, error) {
	var results []OperationTimeMetrics

	// No period floor here (this metric isn't period-scoped), so the sentinel
	// "0001-01-01" defers entirely to the chain's aggregation watermark — see
	// fallbackRawWindowStart's doc comment.
	fallbackStart := fallbackRawWindowStart(aggregatedThrough, "0001-01-01")

	// Combines agrega (complete past days) with raw fallback (days not yet aggregated).
	query := `
		WITH last_down AS (
			SELECT chain_id, addr, MAX(last_down) AS last_down_date FROM (
				SELECT chain_id, addr, block_date::date AS last_down
				FROM daily_participation_agregas WHERE chain_id = ? AND missed_count > 0
				UNION ALL
				SELECT dp.chain_id, dp.addr, dp.date::date AS last_down
				FROM daily_participations dp
				LEFT JOIN daily_participation_agregas dpa
					ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date::date = dp.date::date
				WHERE dp.chain_id = ? AND dp.date >= ? AND dp.participated = false AND dpa.block_date IS NULL
			) GROUP BY chain_id, addr
		),
		last_up AS (
			SELECT chain_id, addr, MAX(last_up) AS last_up_date FROM (
				SELECT chain_id, addr, block_date::date AS last_up
				FROM daily_participation_agregas WHERE chain_id = ? AND participated_count > 0
				UNION ALL
				SELECT dp.chain_id, dp.addr, dp.date::date AS last_up
				FROM daily_participations dp
				LEFT JOIN daily_participation_agregas dpa
					ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date::date = dp.date::date
				WHERE dp.chain_id = ? AND dp.date >= ? AND dp.participated = true AND dpa.block_date IS NULL
			) GROUP BY chain_id, addr
		)
		SELECT
			COALESCE(NULLIF(am.moniker, 'unknown'), fm.moniker, ld.addr) AS moniker,
			ld.addr,
			ld.last_down_date,
			lu.last_up_date,
			ROUND(EXTRACT(EPOCH FROM (lu.last_up_date::timestamp - ld.last_down_date::timestamp)) / 86400.0, 1) AS days_diff
		FROM last_down ld
		LEFT JOIN last_up lu ON lu.chain_id = ld.chain_id AND lu.addr = ld.addr
		LEFT JOIN addr_monikers am ON am.chain_id = ld.chain_id AND am.addr = ld.addr
		LEFT JOIN (
			-- Frozen-moniker fallback (symmetric with the other metric queries):
			-- the aggregate table is small, so this stays cheap.
			SELECT addr, MAX(NULLIF(moniker, '')) AS moniker
			FROM daily_participation_agregas
			WHERE chain_id = ?
			GROUP BY addr
		) fm ON fm.addr = ld.addr`

	if err := db.Raw(query, chainID, chainID, fallbackStart, chainID, chainID, fallbackStart, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}

	return results, nil
}
// aggregatedThrough is the chain's aggregation watermark — see
// GetCurrentPeriodParticipationRate's doc comment.
func UptimeMetricsaddr(db *gorm.DB, chainID string, aggregatedThrough time.Time) ([]UptimeMetrics, error) {
	var results []UptimeMetrics

	// Both the agrega and raw-fallback branches use this same Go-computed
	// bound (rather than the fallback using Go's clock and the agrega branch
	// using Postgres's CURRENT_DATE) so the two branches can never disagree
	// on "30 days ago" due to app/DB clock drift or a midnight-boundary race
	// between when this runs and when the query executes.
	thirtyDaysAgo := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")
	fallbackStart := fallbackRawWindowStart(aggregatedThrough, thirtyDaysAgo)

	// Same symmetric moniker fallback as the participation query: live
	// addr_monikers → frozen row moniker → address last.
	query := `
		SELECT combined.addr,
			MAX(COALESCE(NULLIF(am.moniker, 'unknown'), NULLIF(combined.moniker, ''), combined.addr)) AS moniker,
			100.0 * SUM(combined.participated_count) / NULLIF(SUM(combined.total_blocks), 0) AS uptime
		FROM (
			SELECT chain_id, addr, moniker, participated_count, total_blocks
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date::date >= ? AND block_date::date < CURRENT_DATE
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				MAX(dp.moniker),
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				COUNT(*)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date::date = dp.date::date
			WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < CURRENT_DATE
				AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				MAX(dp.moniker),
				SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END),
				COUNT(*)
			FROM daily_participations dp
			WHERE dp.chain_id = ? AND dp.date >= CURRENT_DATE AND dp.date < CURRENT_DATE + INTERVAL '1 day'
			GROUP BY dp.chain_id, dp.addr
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr
		ORDER BY uptime ASC`
	if err := db.Raw(query, chainID, thirtyDaysAgo, chainID, fallbackStart, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request Uptime: %s", err)
	}
	return results, nil
}

// aggregatedThrough is the chain's aggregation watermark — see
// GetCurrentPeriodParticipationRate's doc comment.
func TxContrib(db *gorm.DB, chainID, period string, aggregatedThrough time.Time) ([]TxContribMetrics, error) {
	var results []TxContribMetrics

	startStr, _, err := getPeriodParams(period)
	if err != nil {
		log.Printf("[db] invalid period: %v", err)
		return nil, err
	}
	fallbackStart := fallbackRawWindowStart(aggregatedThrough, startStr)

	// Same symmetric moniker fallback as the participation query. Raw-fallback
	// branch bound via fallbackRawWindowStart — see that function's doc comment.
	query := `
		WITH combined AS (
			SELECT chain_id, addr, moniker, tx_contribution_count
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ? AND block_date::date < CURRENT_DATE
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				MAX(dp.moniker),
				SUM(CASE WHEN dp.tx_contribution THEN 1 ELSE 0 END)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date::date = dp.date::date
			WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < CURRENT_DATE AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				MAX(dp.moniker),
				SUM(CASE WHEN dp.tx_contribution THEN 1 ELSE 0 END)
			FROM daily_participations dp
			WHERE dp.chain_id = ? AND dp.date >= CURRENT_DATE AND dp.date < CURRENT_DATE + INTERVAL '1 day'
			GROUP BY dp.chain_id, dp.addr
		),
		total AS (SELECT NULLIF(SUM(tx_contribution_count), 0) AS total_tx FROM combined)
		SELECT combined.addr,
			MAX(COALESCE(NULLIF(am.moniker, 'unknown'), NULLIF(combined.moniker, ''), combined.addr)) AS moniker,
			ROUND(SUM(combined.tx_contribution_count) * 100.0 / (SELECT total_tx FROM total), 1) AS tx_contrib
		FROM combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr`

	if err := db.Raw(query, chainID, startStr, chainID, fallbackStart, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request TxContrib: %s", err)
	}

	return results, nil
}

// ===================================== MISSING BLOCK =============================
// aggregatedThrough is the chain's aggregation watermark — see
// GetCurrentPeriodParticipationRate's doc comment.
func MissingBlock(db *gorm.DB, chainID, period string, aggregatedThrough time.Time) ([]MissingBlockMetrics, error) {
	var results []MissingBlockMetrics

	startStr, _, err := getPeriodParams(period)
	if err != nil {
		log.Printf("[db] invalid period: %v", err)
		return nil, err
	}
	fallbackStart := fallbackRawWindowStart(aggregatedThrough, startStr)

	// Same symmetric moniker fallback as the participation query. Raw-fallback
	// branch bound via fallbackRawWindowStart — see that function's doc comment.
	query := `
		SELECT combined.addr,
			MAX(COALESCE(NULLIF(am.moniker, 'unknown'), NULLIF(combined.moniker, ''), combined.addr)) AS moniker,
			SUM(combined.missed_count) AS missing_block
		FROM (
			SELECT chain_id, addr, moniker, missed_count
			FROM daily_participation_agregas
			WHERE chain_id = ? AND block_date >= ? AND block_date::date < CURRENT_DATE
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				MAX(dp.moniker),
				SUM(CASE WHEN dp.participated = false THEN 1 ELSE 0 END)
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date::date = dp.date::date
			WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < CURRENT_DATE AND dpa.block_date IS NULL
			GROUP BY dp.chain_id, dp.addr
			UNION ALL
			SELECT dp.chain_id, dp.addr,
				MAX(dp.moniker),
				SUM(CASE WHEN dp.participated = false THEN 1 ELSE 0 END)
			FROM daily_participations dp
			WHERE dp.chain_id = ? AND dp.date >= CURRENT_DATE AND dp.date < CURRENT_DATE + INTERVAL '1 day'
			GROUP BY dp.chain_id, dp.addr
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr`

	if err := db.Raw(query, chainID, startStr, chainID, fallbackStart, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request MissingBlock: %s", err)
	}

	return results, nil
}

// GetMissedBlocksWindow returns the count of missed blocks per validator
// within the given time window (since = time.Now() - duration).
func GetMissedBlocksWindow(db *gorm.DB, chainID string, since time.Time) ([]MissingBlockMetrics, error) {
	var results []MissingBlockMetrics
	// dp.date is a timestamptz column: pass the time.Time bound directly rather
	// than formatting to a string.
	query := `
		SELECT
			MAX(COALESCE(am.moniker, dp.addr)) AS moniker,
			dp.addr,
			SUM(CASE WHEN dp.participated = false THEN 1 ELSE 0 END) AS missing_block
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ?
		  AND dp.date >= ?
		GROUP BY dp.addr`
	if err := db.Raw(query, chainID, since.UTC()).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in GetMissedBlocksWindow: %w", err)
	}
	return results, nil
}

// MissedMultiWindow holds missed-block counts for one validator across the 1h,
// 24h and 7d windows, computed in a single scan.
type MissedMultiWindow struct {
	Addr      string `gorm:"column:addr"`
	Moniker   string `gorm:"column:moniker"`
	Missed1h  int    `gorm:"column:missed_1h"`
	Missed24h int    `gorm:"column:missed_24h"`
	Missed7d  int    `gorm:"column:missed_7d"`
}

// GetMissedBlocksMultiWindow returns per-validator missed-block counts for the
// 1h, 24h and 7d windows in one query, replacing three separate
// GetMissedBlocksWindow scans. The outer WHERE bounds the scan to the widest
// (7d) window; the per-window counts use FILTER so narrower windows are exact.
func GetMissedBlocksMultiWindow(db *gorm.DB, chainID string) ([]MissedMultiWindow, error) {
	var results []MissedMultiWindow
	query := `
		SELECT
			dp.addr,
			MAX(COALESCE(am.moniker, dp.addr)) AS moniker,
			COUNT(*) FILTER (WHERE dp.participated = false AND dp.date >= NOW() - INTERVAL '1 hour')   AS missed_1h,
			COUNT(*) FILTER (WHERE dp.participated = false AND dp.date >= NOW() - INTERVAL '24 hours') AS missed_24h,
			COUNT(*) FILTER (WHERE dp.participated = false AND dp.date >= NOW() - INTERVAL '7 days')   AS missed_7d
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ? AND dp.date >= NOW() - INTERVAL '7 days'
		GROUP BY dp.addr`
	if err := db.Raw(query, chainID).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in GetMissedBlocksMultiWindow: %w", err)
	}
	return results, nil
}

// ====================================== ADDR MONIKER =============================

func InsertAddrMoniker(db *gorm.DB, addr, moniker string) error {
	addrmoniker := AddrMoniker{

		Addr:    addr,
		Moniker: moniker,
	}
	return db.Create(&addrmoniker).Error
}
func GetMoniker(db *gorm.DB, chainID string) (map[string]string, error) {
	var entries []AddrMoniker
	result := db.Where("chain_id = ?", chainID).Find(&entries)
	if result.Error != nil {
		return nil, result.Error
	}

	monikerMap := make(map[string]string)
	for _, e := range entries {
		monikerMap[e.Addr] = e.Moniker
	}
	log.Printf("[db] loaded %d monikers", len(monikerMap))
	return monikerMap, nil
}

// GetFirstActiveBlocksMap returns a map of addr -> first_active_block for a chain.
func GetFirstActiveBlocksMap(db *gorm.DB, chainID string) (map[string]int64, error) {
	var entries []AddrMoniker
	result := db.Where("chain_id = ?", chainID).Find(&entries)
	if result.Error != nil {
		return nil, result.Error
	}
	fabMap := make(map[string]int64, len(entries))
	for _, e := range entries {
		fabMap[e.Addr] = e.FirstActiveBlock
	}
	return fabMap, nil
}

// getPeriodParams returns parameterized date boundaries for a given period.
// Returns date strings suitable for use as GORM query parameters (not for fmt.Sprintf).
func getPeriodParams(period string) (startStr, endStr string, err error) {
	var start, end time.Time
	now := time.Now()

	switch period {
	case "current_week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday => 7
		}
		start = now.AddDate(0, 0, -weekday+1) // Return to last Monday
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
		end = start.AddDate(0, 0, 7)

	case "current_month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)

	case "current_year":
		start = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(1, 0, 0)

	case "all_time":
		// Use extreme bounds to cover all data
		start = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		end = now.AddDate(1, 0, 0)

	default:
		return "", "", fmt.Errorf("invalid period: %s", period)
	}

	startStr = start.Format("2006-01-02")
	endStr = end.Format("2006-01-02")
	return startStr, endStr, nil
}

// fallbackRawWindowStart returns the lower date-string bound to use for a
// metric query's raw-fallback branch (the leg that catches daily_participations
// rows not yet rolled into daily_participation_agregas), given the branch's
// otherwise-applicable floor (period start, or a permissive sentinel like
// "0001-01-01" for queries with no period) and the chain's aggregation
// watermark (GetAggregatedThrough).
//
// Everything strictly before the watermark is guaranteed to already be
// covered by the agrega branch — AggregateChain rolls up every complete day
// (< today) hourly — so the fallback branch never needs to look earlier than
// that. Scanning further back just re-confirms "nothing to add" at the cost
// of a full raw-table scan (measured: a few hundred thousand rows read and
// sorted, for zero result rows, on the current_month queries driving the
// Prometheus update loop).
//
// When nothing has been aggregated yet for the chain (new chain, or a test
// fixture with raw-only history and no agrega rows), aggregatedThrough is the
// zero time and this returns floorStr unchanged — the fallback branch keeps
// its original, full-period reach.
//
// Takes the watermark as a plain value rather than computing it itself
// (db, chainID) so callers that invoke several of these metric functions for
// the same chain in one pass (the Prometheus update loop calls all six) can
// look it up once via GetAggregatedThrough and share it, instead of each
// function re-querying daily_participation_agregas independently.
func fallbackRawWindowStart(aggregatedThrough time.Time, floorStr string) string {
	if aggregatedThrough.IsZero() {
		return floorStr
	}
	if aggStr := aggregatedThrough.Format("2006-01-02"); aggStr > floorStr {
		return aggStr
	}
	return floorStr
}

// ====================================== First Seen ======================================

// GetFirstSeen returns the earliest participation date for each validator.
// This can be used as a "start time" / "first seen" metric.
// aggregatedThrough is the chain's aggregation watermark — see
// GetCurrentPeriodParticipationRate's doc comment.
func GetFirstSeen(db *gorm.DB, chainID string, aggregatedThrough time.Time) ([]FirstSeenMetrics, error) {
	var results []FirstSeenMetrics

	// first_seen wants the EARLIEST participation ever, so unlike the
	// period-scoped metrics above there's no natural floor to pass — but
	// fallbackRawWindowStart is still safe here: it only ever narrows the
	// fallback branch up to the chain's aggregation watermark, and everything
	// before that watermark is guaranteed to already be in the agrega branch
	// by construction (AggregateChain rolls up every complete day). So no
	// validator's true first-seen date can be missed.
	fallbackStart := fallbackRawWindowStart(aggregatedThrough, "0001-01-01")

	query := `
		SELECT combined.addr,
			MAX(COALESCE(am.moniker, combined.addr)) AS moniker,
			MIN(combined.first_seen) AS first_seen
		FROM (
			SELECT chain_id, addr, block_date::date AS first_seen
			FROM daily_participation_agregas
			WHERE chain_id = ? AND participated_count > 0
			UNION ALL
			SELECT dp.chain_id, dp.addr, dp.date::date AS first_seen
			FROM daily_participations dp
			LEFT JOIN daily_participation_agregas dpa
				ON dpa.chain_id = dp.chain_id AND dpa.addr = dp.addr AND dpa.block_date::date = dp.date::date
			WHERE dp.chain_id = ? AND dp.date >= ? AND dp.participated = true AND dpa.block_date IS NULL
		) combined
		LEFT JOIN addr_monikers am ON am.chain_id = combined.chain_id AND am.addr = combined.addr
		GROUP BY combined.addr
		ORDER BY first_seen ASC`

	if err := db.Raw(query, chainID, chainID, fallbackStart).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("error in the request FirstSeen: %s", err)
	}

	return results, nil
}

func GetTimeOfBlock(db *gorm.DB, chainID string, numBlock int64) (time.Time, error) {
	var blockTime time.Time

	err := db.Raw(`
		SELECT date
		FROM daily_participations
		WHERE chain_id = ? AND block_height = ?
		LIMIT 1
	`, chainID, numBlock).Scan(&blockTime).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get time of block %d: %w", numBlock, err)
	}

	return blockTime, nil
}
func GetTimeOfAlert(db *gorm.DB, chainID string, numBlock int64) (time.Time, error) {
	var blockTime time.Time

	err := db.Raw(`
		SELECT sent_at
		FROM alert_logs
		WHERE chain_id = ? AND start_height = ? AND end_height = ?
		LIMIT 1
	`, chainID, numBlock, numBlock).Scan(&blockTime).Error
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get time of block %d: %w", numBlock, err)
	}

	return blockTime, nil
}

// ====================================== CHAIN HEALTH METRICS ==============================

// GetActiveValidatorCount returns the count of validators with at least 1 participation
// in the last 100 blocks scanned for the given chain.
func GetActiveValidatorCount(db *gorm.DB, chainID string) (int, error) {
	var maxHeight int64
	if err := db.Raw(`SELECT COALESCE(MAX(block_height), 0) FROM daily_participations WHERE chain_id = ?`, chainID).Scan(&maxHeight).Error; err != nil {
		return 0, fmt.Errorf("error fetching max height for active count: %w", err)
	}
	if maxHeight == 0 {
		return 0, nil
	}

	var count int
	query := `
		SELECT COUNT(DISTINCT addr) AS count
		FROM daily_participations
		WHERE chain_id = ? AND participated = true
		AND block_height > ?`

	err := db.Raw(query, chainID, maxHeight-100).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("error counting active validators: %w", err)
	}

	return count, nil
}

// GetAvgParticipationRate returns the average participation rate (0-100) across all validators
// in the last 100 blocks of the given chain.
func GetAvgParticipationRate(db *gorm.DB, chainID string) (float64, error) {
	var maxHeight int64
	if err := db.Raw(`SELECT COALESCE(MAX(block_height), 0) FROM daily_participations WHERE chain_id = ?`, chainID).Scan(&maxHeight).Error; err != nil {
		return 0.0, fmt.Errorf("error fetching max height for avg rate: %w", err)
	}
	if maxHeight == 0 {
		return 0.0, nil
	}

	var avgRate sql.NullFloat64
	query := `
		SELECT AVG(CASE WHEN participated THEN 1.0 ELSE 0.0 END) * 100 AS avg_rate
		FROM daily_participations
		WHERE chain_id = ?
		AND block_height > ?`

	err := db.Raw(query, chainID, maxHeight-100).Scan(&avgRate).Error
	if err != nil {
		return 0.0, fmt.Errorf("error calculating avg participation rate: %w", err)
	}

	if !avgRate.Valid {
		return 0.0, nil
	}

	return avgRate.Float64, nil
}

// GetCurrentChainHeight returns the latest block height for the given chain.
func GetCurrentChainHeight(db *gorm.DB, chainID string) (int64, error) {
	var height sql.NullInt64

	query := `SELECT MAX(block_height) AS height FROM daily_participations WHERE chain_id = ?`

	err := db.Raw(query, chainID).Scan(&height).Error
	if err != nil {
		return 0, fmt.Errorf("error getting current chain height: %w", err)
	}

	if !height.Valid {
		return 0, nil
	}

	return height.Int64, nil
}

// ====================================== ALERT METRICS ==============================

// GetActiveAlertCount returns the count of currently active alerts (unresolved)
// with the given severity level for the given chain.
// Active = most recent alert for that validator has the given level.
func GetActiveAlertCount(db *gorm.DB, chainID, level string) (int, error) {
	var count int

	// Optimized: Use CTE to find latest alert per validator
	query := `
		WITH latest_alerts AS (
			SELECT addr, MAX(sent_at) as last_sent
			FROM alert_logs
			WHERE chain_id = ?
			GROUP BY addr
		)
		SELECT COUNT(*) as count
		FROM alert_logs al
		INNER JOIN latest_alerts la ON al.addr = la.addr AND al.sent_at = la.last_sent
		WHERE al.chain_id = ? AND al.level = ?`

	err := db.Raw(query, chainID, chainID, level).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("error counting active alerts: %w", err)
	}

	return count, nil
}

// MissedBlockCount holds the count of missed blocks per validator in a time window.
type MissedBlockCount struct {
	Addr    string
	Moniker string
	Missed  int
}

// GetMissedBlocksLast24h returns the count of missed blocks per validator in the
// last 24 hours for the given chain, ordered by missed count descending.
func GetMissedBlocksLast24h(db *gorm.DB, chainID string) ([]MissedBlockCount, error) {
	var result []MissedBlockCount
	// Resolve the moniker from addr_monikers (kept current by the metrics
	// updater) and only fall back to the moniker frozen into the
	// daily_participations row when no override exists, mirroring the sibling
	// queries (CalculateValidatorStatusLast24h, CalculateRate). Without this
	// join a validator named "unknown" when its blocks were recorded keeps
	// showing "unknown" even after its moniker is later resolved.
	err := db.Raw(`
		SELECT dp.addr,
		       COALESCE(MAX(am.moniker), MAX(dp.moniker), '') AS moniker,
		       COUNT(*) AS missed
		FROM daily_participations dp
		LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
		WHERE dp.chain_id = ?
		  AND dp.participated = false
		  AND dp.date >= NOW() - INTERVAL '24 hours'
		GROUP BY dp.addr
		ORDER BY missed DESC
	`, chainID).Scan(&result).Error
	return result, err
}

// GetTotalAlertCount returns the total count of alerts with the given level for the given chain.
func GetTotalAlertCount(db *gorm.DB, chainID, level string) (int64, error) {
	var count int64

	query := `SELECT COUNT(*) FROM alert_logs WHERE chain_id = ? AND level = ?`

	err := db.Raw(query, chainID, level).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("error counting total alerts: %w", err)
	}

	return count, nil
}
