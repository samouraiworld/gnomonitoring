# Feature: Add Enhanced Prometheus Metrics

## Overview
Extend Prometheus metrics to expose all validator performance metrics already calculated for the API and Telegram commands, plus add system-level chain health metrics.

## Current State
**Existing Prometheus metrics** (updated every 5 minutes via `StartMetricsUpdater`):
- `gnoland_missed_blocks` (GaugeVec) - total missed blocks per validator per day
- `gnoland_consecutive_missed_blocks` (GaugeVec) - consecutive missed blocks per validator
- `gnoland_validator_participation_rate` (GaugeVec) - participation rate % per validator

**Metrics calculated for API/Telegram** (not yet exposed to Prometheus):
- `uptime` - % participation on last 500 blocks (via `UptimeMetricsaddr()`)
- `operation_time` - days since last down event (via `OperationTimeMetricsaddr()`)
- `tx_contrib` - % tx contribution in period (via `TxContrib()`)
- `missing_blocks` - total missing blocks in period (via `MissingBlock()`)
- `first_seen` - earliest participation date (via `GetFirstSeen()`)

## Proposed Metrics to Add

### Per-Validator Metrics (with labels: chain, validator_address, moniker)

#### Performance Metrics (from API/Telegram)
1. **gnoland_validator_uptime** (Gauge)
   - % participation on last 500 blocks
   - Data source: `UptimeMetricsaddr()`
   - Example: 99.5 (%)

2. **gnoland_validator_operation_time** (Gauge)
   - Days since last participation failure
   - Data source: `OperationTimeMetricsaddr()`
   - Example: 45.3 (days)

3. **gnoland_validator_tx_contribution** (Gauge)
   - % of block transactions signed in current month
   - Data source: `TxContrib()` with "current_month" period
   - Example: 5.2 (%)

4. **gnoland_validator_missing_blocks_month** (Gauge)
   - Total missing blocks in current month
   - Data source: `MissingBlock()` with "current_month" period
   - Example: 12 (blocks)

#### Temporal Metrics
5. **gnoland_validator_first_seen_unix** (Gauge)
   - Unix timestamp of first participation
   - Data source: `GetFirstSeen()`
   - Helps identify new validators

### Chain-Level Metrics (with label: chain)

6. **gnoland_chain_active_validators** (Gauge)
   - Count of validators with at least 1 participation in last 100 blocks
   - Data source: COUNT(DISTINCT addr) from daily_participations WHERE participated=1

7. **gnoland_chain_avg_participation_rate** (Gauge)
   - Average participation rate across all validators
   - Data source: AVG(participated) of all validators in last 100 blocks
   - Range: 0-100 (%)

8. **gnoland_chain_current_height** (Gauge)
   - Latest block height being tracked
   - Data source: MAX(block_height) from daily_participations
   - Useful to detect if chain is stuck

### Alert Metrics (with labels: chain, level)

9. **gnoland_active_alerts** (Gauge)
   - Count of currently active (CRITICAL/WARNING) alerts
   - Data source: Count of alert_logs WHERE level IN ('CRITICAL', 'WARNING') AND resolved=false
   - Label: chain, level (CRITICAL, WARNING)

10. **gnoland_alerts_total** (Counter)
    - Total alerts sent (incremented, never reset)
    - Data source: alert_logs table
    - Label: chain, level

## Implementation Plan

### Phase 1: Add Validator Metrics (Week 1)
**Files to modify:**
- `internal/gnovalidator/Prometheus.go` - Register new metric definitions
- `internal/gnovalidator/metric.go` - Add calculation functions if needed
- Update `UpdatePrometheusMetricsFromDB()` to populate new metrics

**Metrics to add:**
- gnoland_validator_uptime
- gnoland_validator_operation_time
- gnoland_validator_tx_contribution
- gnoland_validator_missing_blocks_month
- gnoland_validator_first_seen_unix

**Implementation details:**
```go
// New GaugeVecs in Prometheus.go
var (
    ValidatorUptime = prometheus.NewGaugeVec(...)        // %
    ValidatorOperationTime = prometheus.NewGaugeVec(...) // days
    ValidatorTxContribution = prometheus.NewGaugeVec(...) // %
    ValidatorMissingBlocksMonth = prometheus.NewGaugeVec(...) // count
    ValidatorFirstSeenUnix = prometheus.NewGaugeVec(...) // timestamp
)

// In UpdatePrometheusMetricsFromDB(), iterate existing uptime results
// and call the 4 metric calculation functions
uptimeStats, _ := CalculateValidatorRates(db, chainID)  // Already exists!
operationStats, _ := OperationTimeMetricsaddr(db, chainID)
txStats, _ := TxContrib(db, chainID, "current_month")
missingStats, _ := MissingBlock(db, chainID, "current_month")
firstSeenStats, _ := GetFirstSeen(db, chainID)

// Set each metric with loop and WithLabelValues
```

**Testing:**
- Write unit tests in `Prometheus_test.go`
- Verify correct period is passed to TxContrib/MissingBlock
- Test label consistency (chain, validator_address, moniker)

---

### Phase 2: Add Chain-Level Metrics (Week 2)
**Files to modify:**
- `internal/gnovalidator/Prometheus.go` - Register chain metrics
- Add query functions in `internal/database/db_metrics.go` if needed

**Metrics to add:**
- gnoland_chain_active_validators
- gnoland_chain_avg_participation_rate
- gnoland_chain_current_height

**Implementation details:**
```go
// New Gauges in Prometheus.go (not GaugeVec, just Gauge with chain label)
var (
    ChainActiveValidators = prometheus.NewGaugeVec(...)      // count
    ChainAvgParticipationRate = prometheus.NewGaugeVec(...)  // %
    ChainCurrentHeight = prometheus.NewGaugeVec(...)         // height
)

// Add helper functions in db_metrics.go
GetActiveValidatorCount(db, chainID string) (int, error)
GetAvgParticipationRate(db, chainID string) (float64, error)

// In UpdatePrometheusMetricsFromDB()
activeCount, _ := GetActiveValidatorCount(db, chainID)
ChainActiveValidators.WithLabelValues(chainID).Set(float64(activeCount))
```

**Testing:**
- Verify count matches current validator set from last 100 blocks
- Verify avg rate is 0-100%
- Verify height increments with new blocks

---

### Phase 3: Add Alert Metrics (Week 3)
**Files to modify:**
- `internal/gnovalidator/Prometheus.go` - Register alert metrics
- `internal/database/db_metrics.go` - Add alert query helpers

**Metrics to add:**
- gnoland_active_alerts (Gauge)
- gnoland_alerts_total (Counter)

**Implementation details:**
```go
// New metric types
var (
    ActiveAlerts = prometheus.NewGaugeVec(...)    // chain, level
    AlertsTotal = prometheus.NewCounterVec(...)   // chain, level
)

// In db_metrics.go
GetActiveAlertCount(db, chainID, level string) (int, error)
GetTotalAlertCount(db, chainID, level string) (int64, error)

// In StartMetricsUpdater()
for _, level := range []string{"CRITICAL", "WARNING"} {
    active, _ := GetActiveAlertCount(db, chainID, level)
    total, _ := GetTotalAlertCount(db, chainID, level)
    ActiveAlerts.WithLabelValues(chainID, level).Set(float64(active))
    AlertsTotal.WithLabelValues(chainID, level).Add(float64(total))
}
```

**Caveats:**
- Counter is incremented on every update cycle → need to track previous count to only increment by delta
- Or: Keep Counter cumulative from DB and reset on app restart (simpler)
- Decision: Start simple—just count alert_logs rows, counter will jump on restart but reflects true state

**Testing:**
- Verify alert count matches alert_logs WHERE level='CRITICAL' AND resolved=false
- Verify counter increases with new alerts

---

## Database Queries

### Get Active Validator Count (last 100 blocks)
```sql
SELECT COUNT(DISTINCT addr)
FROM daily_participations
WHERE chain_id = ? AND participated = 1
AND block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 100
```

### Get Average Participation Rate (last 100 blocks)
```sql
SELECT AVG(CAST(participated AS FLOAT)) * 100
FROM daily_participations
WHERE chain_id = ?
AND block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 100
```

### Get Active Alert Count
```sql
SELECT COUNT(*) FROM alert_logs
WHERE chain_id = ? AND level = ? AND resolved = false
```

---

## Label Consistency

All validator metrics must use these labels:
- `chain` (or `chain_id`) - chain identifier
- `validator_address` (or `addr`) - validator address
- `moniker` - validator moniker

All chain metrics use:
- `chain` - chain identifier

All alert metrics use:
- `chain` - chain identifier
- `level` - alert severity (CRITICAL, WARNING)

*Note:* Ensure label names match naming convention (snake_case preferred for Prometheus)

---

## Update Frequency

- Per-validator metrics: Update every 5 minutes (same as current `StartMetricsUpdater` interval)
- Chain metrics: Update every 5 minutes (tied to same updater goroutine)
- Alert metrics: Update every 5 minutes (cumulative, no reset needed)

---

## Backward Compatibility

All existing metrics remain unchanged:
- `gnoland_missed_blocks`
- `gnoland_consecutive_missed_blocks`
- `gnoland_validator_participation_rate`

New metrics are additive only. No deprecation needed for v1.

---

## Testing Strategy

1. **Unit tests** (`Prometheus_test.go`):
   - Mock DB with known data
   - Verify metric values match expected calculation
   - Verify label values are correct

2. **Integration tests**:
   - Run with real SQLite test DB (via `testoutils.NewTestDB()`)
   - Populate with known validator data
   - Verify all metrics computed correctly
   - Verify `/metrics` endpoint returns all new metrics

3. **Manual testing**:
   - Curl `/metrics` and grep for each metric name
   - Verify values make sense (uptime 0-100%, operation_time > 0, etc.)
   - Test per-chain isolation

---

## Deliverables

1. **Code changes:**
   - Modified `Prometheus.go` with 10 new metric definitions
   - Modified `metric.go` with any new calculation helpers
   - Modified `db_metrics.go` with chain-level query functions
   - Updated `UpdatePrometheusMetricsFromDB()` to populate all metrics

2. **Tests:**
   - `Prometheus_test.go` unit tests for each metric calculation
   - Integration test in `gnovalidator_realtime_test.go` or new file

3. **Documentation:**
   - Update CLAUDE.md with new metric descriptions
   - Add comments in code for each metric

---

## Risk & Mitigation

| Risk | Impact | Mitigation |
|------|--------|-----------|
| Query performance on large DBs | Metrics update slow/timeout | Use indexed queries; test with 1M+ rows |
| Label cardinality explosion | Memory bloat in Prometheus | Limit to chain + validator + moniker; monitor |
| Alert metrics become stale | Incorrect alert state | Use Gauge (reflects current state); update every 5 min |
| Cross-chain label confusion | Wrong data in dashboard | Add chain label to ALL metrics; test per-chain |

---

## Success Criteria

- ✅ All 10 new metrics appear in `/metrics` endpoint
- ✅ Each metric has correct labels and values
- ✅ Metrics update every 5 minutes
- ✅ No performance degradation (update takes < 1 second)
- ✅ All tests pass
- ✅ No breaking changes to existing metrics
- ✅ Multi-chain isolation verified (each chain has separate metric series)
