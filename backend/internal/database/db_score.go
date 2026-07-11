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

// periodBounds returns [start,end) for a report period. All bounds are derived
// in UTC (independent of the process's local timezone) so they line up with the
// UTC block timestamps stored in daily_participations and the UTC-formatted
// block_date boundaries used by the raw/aggregate partition below.
func periodBounds(period string, now time.Time) (time.Time, time.Time, error) {
	now = now.UTC()
	switch period {
	case "last_24h":
		return now.Add(-24 * time.Hour), now, nil
	case "current_week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -weekday+1)
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

// periodPartition describes how a report period splits across the durable daily
// aggregate (complete past days) and the raw current-day rows, with the seam
// fixed at today 00:00 UTC to avoid double counting.
type periodPartition struct {
	rawStart, end          time.Time // raw window [rawStart, end)
	agregaStart, agregaEnd string    // aggregate window [agregaStart, agregaEnd) as YYYY-MM-DD
	includeAgrega          bool
}

// computePartition derives the raw/aggregate windows for a report period. All
// bounds are UTC so they line up with the UTC block timestamps and block_date
// day strings. The seam between the two arms is anchored to aggregatedThrough
// — the actual exclusive upper bound of days rolled up into
// daily_participation_agregas (see GetAggregatedThrough) — rather than to
// wall-clock "today". This makes the split self-healing when the hourly
// aggregator lags behind midnight: any day not yet aggregated is picked up by
// the raw arm instead of falling into the gap between the two arms.
func computePartition(period string, now, aggregatedThrough time.Time) (periodPartition, error) {
	start, end, err := periodBounds(period, now)
	if err != nil {
		return periodPartition{}, err
	}

	rawStart := aggregatedThrough
	if period == "last_24h" || aggregatedThrough.IsZero() {
		rawStart = start
	}
	if rawStart.Before(start) {
		rawStart = start
	}

	agregaEnd := aggregatedThrough
	if agregaEnd.After(end) {
		agregaEnd = end
	}

	return periodPartition{
		rawStart:      rawStart,
		end:           end,
		agregaStart:   start.Format("2006-01-02"),
		agregaEnd:     agregaEnd.Format("2006-01-02"),
		includeAgrega: period != "last_24h" && !aggregatedThrough.IsZero() && aggregatedThrough.After(start),
	}, nil
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

// ParticipationRaw holds summed signing (and proposer) activity for one
// validator over one period. Scoped to chain_id.
type ParticipationRaw struct {
	Addr           string `json:"addr"`
	SignedBlocks   int64  `json:"signed_blocks"`
	TotalBlocks    int64  `json:"total_blocks"`
	ProposedBlocks int64  `json:"proposed_blocks"`
}

// GetAggregatedThrough returns the exclusive upper bound (UTC midnight) of the
// days that have already been rolled up into daily_participation_agregas for
// chainID — i.e. the day after the latest aggregated block_date. A zero
// time.Time means nothing has been aggregated yet for this chain, so callers
// must treat the entire period as raw. Anchoring the report's raw/aggregate
// seam to this value (instead of wall-clock "today") keeps it correct even
// when the hourly aggregator run is delayed or the chain is brand new.
func GetAggregatedThrough(db *gorm.DB, chainID string) (time.Time, error) {
	var maxDate *string
	if err := db.Raw(
		`SELECT MAX(block_date) FROM daily_participation_agregas WHERE chain_id = ?`,
		chainID,
	).Scan(&maxDate).Error; err != nil {
		return time.Time{}, fmt.Errorf("GetAggregatedThrough(%s): %w", chainID, err)
	}
	if maxDate == nil {
		return time.Time{}, nil
	}
	d, err := time.ParseInLocation("2006-01-02", *maxDate, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("GetAggregatedThrough(%s): parse %q: %w", chainID, *maxDate, err)
	}
	return d.AddDate(0, 0, 1), nil
}

// GetValidatorParticipation returns per-validator signed/total (and proposed)
// block counts for the period, plus the chain's total block count over the same
// period (used to size expected proposal counts). It reads durable daily
// aggregates for complete past days and raw rows for the current day,
// partitioned at today 00:00 UTC to avoid double counting. last_24h reads only
// raw rows (block granularity). Scoped to chain_id.
func GetValidatorParticipation(db *gorm.DB, chainID, period string) ([]ParticipationRaw, int64, error) {
	aggregatedThrough, err := GetAggregatedThrough(db, chainID)
	if err != nil {
		return nil, 0, err
	}
	p, err := computePartition(period, time.Now(), aggregatedThrough)
	if err != nil {
		return nil, 0, err
	}

	// Participation arms: raw current-day rows, plus aggregate past days.
	combined := `
		SELECT addr,
		       CASE WHEN participated THEN 1 ELSE 0 END AS signed,
		       1 AS total,
		       CASE WHEN proposed THEN 1 ELSE 0 END AS proposed
		FROM daily_participations
		WHERE chain_id = ? AND date >= ? AND date < ?`
	args := []any{chainID, p.rawStart, p.end}
	if p.includeAgrega {
		combined += `
		UNION ALL
		SELECT addr, participated_count AS signed, total_blocks AS total, proposed_count AS proposed
		FROM daily_participation_agregas
		WHERE chain_id = ? AND block_date >= ? AND block_date < ?`
		args = append(args, chainID, p.agregaStart, p.agregaEnd)
	}

	// Chain-block scalar: distinct raw heights, plus (only when the aggregate
	// window applies) the per-day chain block count summed over past days. This
	// matches the semantics of the former GetChainTotalBlocks exactly.
	cb := `SELECT (SELECT COUNT(DISTINCT block_height) FROM daily_participations
	               WHERE chain_id = ? AND date >= ? AND date < ?)`
	args = append(args, chainID, p.rawStart, p.end)
	if p.includeAgrega {
		cb += ` + COALESCE((SELECT SUM(day_blocks) FROM (
		            SELECT MAX(total_blocks) AS day_blocks
		            FROM daily_participation_agregas
		            WHERE chain_id = ? AND block_date >= ? AND block_date < ?
		            GROUP BY block_date) t), 0)`
		args = append(args, chainID, p.agregaStart, p.agregaEnd)
	}
	cb += ` AS chain_blocks`

	q := `
		SELECT combined.addr AS addr,
		       SUM(combined.signed)   AS signed_blocks,
		       SUM(combined.total)    AS total_blocks,
		       SUM(combined.proposed) AS proposed_blocks,
		       cb.chain_blocks        AS chain_blocks
		FROM (` + combined + `) combined
		CROSS JOIN (` + cb + `) cb
		GROUP BY combined.addr, cb.chain_blocks
		ORDER BY combined.addr`

	type scanRow struct {
		Addr           string
		SignedBlocks   int64
		TotalBlocks    int64
		ProposedBlocks int64
		ChainBlocks    int64
	}
	var scanned []scanRow
	if err := db.Raw(q, args...).Scan(&scanned).Error; err != nil {
		return nil, 0, fmt.Errorf("GetValidatorParticipation(%s,%s): %w", chainID, period, err)
	}

	rows := make([]ParticipationRaw, len(scanned))
	var chainBlocks int64
	for i, s := range scanned {
		rows[i] = ParticipationRaw{
			Addr:           s.Addr,
			SignedBlocks:   s.SignedBlocks,
			TotalBlocks:    s.TotalBlocks,
			ProposedBlocks: s.ProposedBlocks,
		}
		chainBlocks = s.ChainBlocks // identical on every row (grouped scalar)
	}
	return rows, chainBlocks, nil
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

// GetValidatorVP returns the current voting power per validator plus the sum
// and max across the chain, used for score severity and proposer-share. Scoped
// to chain_id.
func GetValidatorVP(db *gorm.DB, chainID string) (map[string]int64, int64, int64, error) {
	type row struct {
		Addr        string
		VotingPower int64
	}
	var rows []row
	if err := db.Raw(`
		SELECT addr, voting_power FROM addr_monikers
		WHERE chain_id = ? AND voting_power > 0
	`, chainID).Scan(&rows).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("GetValidatorVP(%s): %w", chainID, err)
	}
	perAddr := make(map[string]int64, len(rows))
	var sum, max int64
	for _, r := range rows {
		perAddr[r.Addr] = r.VotingPower
		sum += r.VotingPower
		if r.VotingPower > max {
			max = r.VotingPower
		}
	}
	return perAddr, sum, max, nil
}

// GetLastAlertTimes returns, per validator, the timestamp of its most recent
// WARNING or CRITICAL alert on the chain. Chain-scoped. The chain-wide
// "blockchain stuck" rows (addr = 'all') are excluded — they don't reflect an
// individual validator's health. Validators with no such alert are absent from
// the map.
func GetLastAlertTimes(db *gorm.DB, chainID string) (map[string]time.Time, error) {
	type row struct {
		Addr      string
		LastAlert time.Time
	}
	var rows []row
	if err := db.Raw(`
		SELECT addr, MAX(sent_at) AS last_alert
		FROM alert_logs
		WHERE chain_id = ?
		  AND level IN ('WARNING','CRITICAL')
		  AND addr <> 'all'
		GROUP BY addr
	`, chainID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("GetLastAlertTimes(%s): %w", chainID, err)
	}
	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		out[r.Addr] = r.LastAlert
	}
	return out, nil
}
