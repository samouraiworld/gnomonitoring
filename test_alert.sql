-- =============================================================================
-- test_alert.sql — Simulate validator alert scenarios
-- =============================================================================
-- Thresholds: WARNING = 5 consecutive missed, CRITICAL = 30+
-- The alert loop reads rows WHERE date >= datetime('now', '-24 hours')
-- =============================================================================

-- Find recent consecutive blocks for a validator to UPDATE
SELECT block_height, datetime(date) AS date, participated
FROM daily_participations
WHERE chain_id = 'test12'
  AND addr = 'g1z9eedz4qfru6ggdsyj7yn85s5ewvdr5gr39c7r'
  AND date >= datetime('now', '-24 hours')
ORDER BY block_height DESC
LIMIT 50;

-- =============================================================================
-- SCENARIO 1 — WARNING : set last 5 blocks to participated=0
-- =============================================================================

UPDATE daily_participations
SET participated = 0
WHERE chain_id = 'test12'
  AND addr = 'g1z9eedz4qfru6ggdsyj7yn85s5ewvdr5gr39c7r'

  AND block_height > (
      SELECT MAX(block_height) - 6
      FROM daily_participations
      WHERE chain_id = 'test12'
        AND addr = 'g1z9eedz4qfru6ggdsyj7yn85s5ewvdr5gr39c7r'
        
  );

-- =============================================================================
-- SCENARIO 2 — CRITICAL : set last 30 blocks to participated=0
-- =============================================================================

UPDATE daily_participations
SET participated = 0
WHERE chain_id = 'test12'
  AND addr = 'g15atj32de45nqgm68298aua8ayy4aujwyewegvd'

  AND block_height > (
      SELECT MAX(block_height) - 30
      FROM daily_participations
      WHERE chain_id = 'test12'
        AND addr = 'g15atj32de45nqgm68298aua8ayy4aujwyewegvd'

  );

-- =============================================================================
-- SCENARIO 3 — RESOLVED : restore participated=1 on the very last block
-- (run after scenario 1 or 2 and waiting for the alert to fire)
-- =============================================================================

UPDATE daily_participations
SET participated = 1
WHERE chain_id = 'test12'
  AND addr = 'g1z9eedz4qfru6ggdsyj7yn85s5ewvdr5gr39c7r'
  AND block_height = (
      SELECT MAX(block_height)
      FROM daily_participations
      WHERE chain_id = 'test12'
        AND addr = 'g1z9eedz4qfru6ggdsyj7yn85s5ewvdr5gr39c7r'
     
  );

-- =============================================================================
-- CLEANUP — Restore all rows to participated=1
-- =============================================================================

UPDATE daily_participations
SET participated = 1
WHERE chain_id = 'test12'
  AND addr = 'g1z9eedz4qfru6ggdsyj7yn85s5ewvdr5gr39c7r'
  AND date >= datetime('now', '-24 hours');

-- =============================================================================
-- VERIFICATION
-- =============================================================================

-- Check current streak seen by alert system
SELECT id, level, start_height, end_height, skipped, datetime(sent_at) AS sent_at
FROM alert_logs
WHERE chain_id = 'test12'
  AND addr = 'g1z9eedz4qfru6ggdsyj7yn85s5ewvdr5gr39c7r'
ORDER BY sent_at DESC
LIMIT 20;
