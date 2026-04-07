# Multi-Chain Migration - Complete Guide

**Date:** 2026-03-19
**Version:** Phase 9 Complete
**Scope:** Production migration guide from single-chain to multi-chain

---

## 📋 Summary of Schema Changes

### Modified Tables (14 total)

| Table | Added Columns | Migrations Required | Impact |
|-------|-------------------|-------------------|--------|
| `daily_participations` | `chain_id` | ✅ ALTER TABLE | CRITICAL |
| `alert_logs` | `chain_id` | ✅ ALTER TABLE | CRITICAL |
| `addr_monikers` | `chain_id` | ✅ ALTER TABLE | CRITICAL |
| `govdaos` | `chain_id` | ✅ ALTER TABLE | CRITICAL |
| `telegram_hour_reports` | `chain_id` | ✅ ALTER TABLE | HIGH |
| `telegram_validator_subs` | `chain_id` | ✅ ALTER TABLE | HIGH |
| `webhook_validators` | `chain_id` | ✅ ALTER TABLE (optional) | MEDIUM |
| `webhook_gov_daos` | `chain_id` | ✅ ALTER TABLE (optional) | MEDIUM |
| `telegrams` | `chain_id` | ✅ ALTER TABLE | HIGH |
| `alert_contacts` | `chain_id` | ✅ ALTER TABLE (optional) | LOW |
| `hour_reports` | `chain_id` | ✅ ALTER TABLE (optional) | MEDIUM |

---

## 🔄 Migration Steps IN PRODUCTION

### Phase 1: Preparation (BEFORE deployment)

```bash
# 1. Backup the current DB
cp backend/db/webhooks.db backend/db/webhooks.db.backup-2026-03-19

# 2. Check existing schema
sqlite3 backend/db/webhooks.db ".tables"
sqlite3 backend/db/webhooks.db ".schema daily_participations"

# 3. Determine the CURRENT default chain
# If your current deployment uses a single chain:
# - Betanet → default_chain: "betanet"
# - Gnoland1 → default_chain: "gnoland1"
```

### Phase 2: Deploy the new code version

```bash
# 1. Update config.yaml
# OLD:
# rpc_endpoint: "https://rpc.betanet.gno.land"
# graphql: "https://indexer.betanet.gno.land/graphql/query"
# gnoweb: "https://betanet.gno.land"

# NEW:
# default_chain: "betanet"  # IMPORTANT: Adjust based on your setup
# chains:
#   betanet:
#     rpc_endpoint: "https://rpc.betanet.gno.land"
#     graphql: "https://indexer.betanet.gno.land/graphql/query"
#     gnoweb: "https://betanet.gno.land"
#     enabled: true
#   gnoland1:
#     rpc_endpoint: "https://rpc.gno.land"
#     graphql: "https://indexer.gno.land/graphql/query"
#     gnoweb: "https://gno.land"
#     enabled: false  # Enable if needed

# 2. Stop the application
systemctl stop gnomonitoring  # or your startup script

# 3. Deploy the new binary
go build -o /usr/local/bin/gnomonitoring ./backend

# 4. Start the application (it will apply migrations)
systemctl start gnomonitoring
```

### Phase 3: Verify migrations

```bash
# 1. Verify that chain_id was added to daily_participations
sqlite3 backend/db/webhooks.db ".schema daily_participations"
# You should see: chain_id TEXT NOT NULL DEFAULT 'betanet'

# 2. Check data
sqlite3 backend/db/webhooks.db "SELECT DISTINCT chain_id FROM daily_participations LIMIT 5;"
# Must return: betanet (or your default chain)

# 3. Verify that indexes were created
sqlite3 backend/db/webhooks.db "SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'idx_dp_%';"
```

---

## 🗺️ Detailed Tables and Migrations

### 1. CRITICAL: `daily_participations`

**Old structure:**
```sql
CREATE TABLE daily_participations (
    date DATETIME,
    block_height INTEGER,
    moniker TEXT,
    addr TEXT NOT NULL,
    participated NUMERIC NOT NULL,
    tx_contribution NUMERIC NOT NULL
);
-- Unique Index: (addr, block_height)
```

**New structure:**
```sql
ALTER TABLE daily_participations ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
-- New Unique Index: (chain_id, addr, block_height)
-- Old Index: (addr, block_height) — TO BE REMOVED
```

**Migration SQL (if manual):**
```sql
-- 1. Add the column
ALTER TABLE daily_participations ADD COLUMN chain_id TEXT DEFAULT 'betanet';

-- 2. Update data (if needed)
UPDATE daily_participations SET chain_id = 'betanet' WHERE chain_id IS NULL;

-- 3. Make NOT NULL
ALTER TABLE daily_participations MODIFY chain_id TEXT NOT NULL;

-- 4. Create new indexes
CREATE INDEX idx_dp_chain_block_height ON daily_participations(chain_id, block_height);
CREATE INDEX idx_dp_chain_addr ON daily_participations(chain_id, addr);
CREATE INDEX idx_dp_chain_date ON daily_participations(chain_id, date);
CREATE INDEX idx_dp_chain_addr_participated ON daily_participations(chain_id, addr, participated);

-- 5. Create new unique constraint
CREATE UNIQUE INDEX uniq_chain_addr_height ON daily_participations(chain_id, addr, block_height);

-- 6. Remove old index (SQLite does not support constraints)
-- Note: SQLite does not allow dropping automatic indexes
```

**Impact:**
- ✅ Zero data loss
- ✅ All existing rows → `chain_id = 'betanet'`
- ✅ Backward compatible (if keeping `chain_id = NULL`)

---

### 2. CRITICAL: `alert_logs`

**Migration SQL:**
```sql
ALTER TABLE alert_logs ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
CREATE INDEX idx_al_chain_addr ON alert_logs(chain_id, addr);
```

**Impact:**
- ✅ Zero data loss
- ✅ New alerts will be correctly scoped by chain

---

### 3. CRITICAL: `addr_monikers`

**Migration SQL:**
```sql
ALTER TABLE addr_monikers ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
CREATE UNIQUE INDEX uniq_chain_addr ON addr_monikers(chain_id, addr);
```

**Impact:**
- ✅ Each chain can have its own version of the moniker for an address
- ⚠️ Existing monikers will be associated with 'betanet'

---

### 4. CRITICAL: `govdaos`

**Migration SQL:**
```sql
ALTER TABLE govdaos ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
```

**Impact:**
- ✅ Future proposals will be scoped by chain
- ✅ Existing history will remain linked to betanet

---

### 5. HIGH: `telegrams`

**Migration SQL:**
```sql
ALTER TABLE telegrams ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
```

**Impact:**
- ✅ Each Telegram chat can now have different preferences per chain
- ✅ Existing preferences will be associated with betanet

---

### 6. HIGH: `telegram_hour_reports`

**Migration SQL:**
```sql
ALTER TABLE telegram_hour_reports ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
```

**Impact:**
- ✅ Each chat can receive reports for multiple chains
- ✅ Existing reports will continue to work

---

### 7. HIGH: `telegram_validator_subs`

**Migration SQL:**
```sql
ALTER TABLE telegram_validator_subs ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
CREATE UNIQUE INDEX idx_tvs_chain_addr_chatid ON telegram_validator_subs(chain_id, addr, chat_id);
```

**Impact:**
- ✅ Each chat can subscribe to the same validator on multiple chains
- ⚠️ Existing subscriptions will be linked to betanet

---

### 8. MEDIUM: `webhook_validators` & `webhook_gov_daos`

**Migration SQL:**
```sql
ALTER TABLE webhook_validators ADD COLUMN chain_id TEXT DEFAULT NULL;
ALTER TABLE webhook_gov_daos ADD COLUMN chain_id TEXT DEFAULT NULL;
```

**Impact:**
- ✅ `chain_id = NULL` → receives alerts from ALL chains
- ✅ `chain_id = 'betanet'` → receives alerts from betanet only
- ✅ Backward compatible (NULL = old behavior)

---

## ⚠️ Critical Points to Check

### 1. Check `config.yaml` BEFORE migration

```yaml
# ❌ OLD (will BREAK):
rpc_endpoint: "https://rpc.betanet.gno.land"
graphql: "https://indexer.betanet.gno.land/graphql/query"
gnoweb: "https://betanet.gno.land"

# ✅ NEW (required):
default_chain: "betanet"
chains:
  betanet:
    rpc_endpoint: "https://rpc.betanet.gno.land"
    graphql: "https://indexer.betanet.gno.land/graphql/query"
    gnoweb: "https://betanet.gno.land"
    enabled: true
```

### 2. Determine the CORRECT `default_chain`

Which chain does your current deployment use? Select the exact name:

```bash
# Determine automatically
curl -s $(cat /path/to/config.yaml | grep rpc_endpoint | cut -d'"' -f2) /status
# Indicate the chain in the logs

# Or check manually in the DB
sqlite3 backend/db/webhooks.db "SELECT COUNT(*) as proposal_count FROM govdaos;"
# Many proposals → This is probably your current chain
```

### 3. Check Webhooks

```bash
# Count existing webhooks
sqlite3 backend/db/webhooks.db "SELECT 'validators' as type, COUNT(*) FROM webhook_validators UNION SELECT 'govdao' as type, COUNT(*) FROM webhook_gov_daos;"

# Result: After migration, all will have chain_id = NULL (receive all alerts)
# This is BACKWARD COMPATIBLE
```

---

## 🔙 ROLLBACK: Return to Single-Chain

If you need to revert to the single-chain version:

### Option 1: Restore from Backup

```bash
# 1. Stop the application
systemctl stop gnomonitoring

# 2. Restore the backup
cp backend/db/webhooks.db.backup-2026-03-19 backend/db/webhooks.db

# 3. Revert to old code
git checkout main -- backend/
go build -o /usr/local/bin/gnomonitoring ./backend

# 4. Restart
systemctl start gnomonitoring
```

### Option 2: Remove `chain_id` columns (NOT RECOMMENDED)

```bash
# ⚠️ VERY DESTRUCTIVE - Chain data will be lost

# For SQLite, you must recreate tables (no ALTER TABLE DROP COLUMN)
-- This is complex and risky. Use Option 1 instead.
```

---

## 📊 Migration Checklist

### PRE-MIGRATION
- [ ] Backup `webhooks.db`
- [ ] Check `config.yaml` structure (old format)
- [ ] Determine `default_chain` to use
- [ ] Test new config.yaml in DEV
- [ ] Check existing webhooks (count)
- [ ] Check existing Telegram chats (count)

### MIGRATION
- [ ] Stop gnomonitoring
- [ ] Update `config.yaml`
- [ ] Deploy new binary
- [ ] Start gnomonitoring (migrations applied automatically)
- [ ] Check logs for errors

### POST-MIGRATION
- [ ] Verify `chain_id` was added everywhere
- [ ] Test API: `GET /Participation?chain=betanet&address=...`
- [ ] Test Telegram: `/chain` command
- [ ] Test GovDAO: `/chain` command
- [ ] Verify webhooks receive alerts
- [ ] Verify Telegram reports work
- [ ] Archive backup: `webhooks.db.backup-2026-03-19`

---

## 🎯 Summary Table of Changes

### Configuration

| Element | Old | New | Action |
|---------|--------|---------|--------|
| Config format | Flat (rpc_endpoint, graphql) | Hierarchical (chains) | Rewrite config.yaml |
| Number of chains | 1 | N (enabled: true) | Add new chains |
| Default chain | Implicit (first) | Explicit (default_chain) | Specify in config |

### Database

| Table | Before | After | Data | Migration |
|-------|--------|-------|---------|-----------|
| daily_participations | no chain_id | with chain_id | Set to 'betanet' | ALTER TABLE |
| alert_logs | no chain_id | with chain_id | Set to 'betanet' | ALTER TABLE |
| addr_monikers | no chain_id | with chain_id | Set to 'betanet' | ALTER TABLE |
| telegram_* | no chain_id | with chain_id | Set to 'betanet' | ALTER TABLE |
| webhooks_* | no chain_id | with chain_id (NULL) | Unchanged | ALTER TABLE |

### API

| Endpoint | Before | After | Change |
|----------|--------|-------|--------|
| GET /Participation | Global | ?chain=betanet | Optional param |
| GET /uptime | Global | ?chain=betanet | Optional param |
| POST /webhooks | Global scope | chain_id (optional) | Chain scoping |
| GET /info | Single endpoints | Array of chains | New format |

### Telegram

| Command | Before | After | Behavior |
|---------|--------|-------|---|
| /subscribe | Single chain | Per-chat choice | Uses /setchain |
| /status | Single chain | Per-chat choice | Uses /setchain |
| /chain | N/A | List chains | NEW |
| /setchain | N/A | Switch chain | NEW |

---

## 📝 Useful Verification Commands

```bash
# 1. Check DB after migration
sqlite3 backend/db/webhooks.db "PRAGMA table_info(daily_participations);"

# 2. Count rows per chain
sqlite3 backend/db/webhooks.db "SELECT chain_id, COUNT(*) FROM daily_participations GROUP BY chain_id;"

# 3. Check indexes
sqlite3 backend/db/webhooks.db ".indices"

# 4. Check DB size
ls -lh backend/db/webhooks.db

# 5. Check webhooks
sqlite3 backend/db/webhooks.db "SELECT type, COUNT(*), COUNT(CASE WHEN chain_id IS NOT NULL THEN 1 END) FROM webhook_validators GROUP BY type;"
```

---

## 🚨 Common Errors and Solutions

### Error 1: `column chain_id already exists`
**Cause:** The column was already added
**Solution:** Check the schema, the migration is idempotent

### Error 2: `config.yaml: missing field rpc_endpoint`
**Cause:** Config format not updated
**Solution:** Use the hierarchical format with `chains:`

### Error 3: Webhooks receive nothing
**Cause:** chain_id = NULL (correct) but handlers don't filter
**Solution:** Restart the application, check logs

### Error 4: Telegram commands /chain, /setchain not found
**Cause:** Old binary in memory or not compiled
**Solution:** `go build ./...` + `systemctl restart gnomonitoring`

---

## 📞 Support and Questions

For questions about migration:
1. Verify that `config.yaml` is correct
2. Verify that `default_chain` matches your setup
3. Check logs: `journalctl -u gnomonitoring -f`
4. Use the verification commands above

---

**Status:** ✅ Multi-Chain Migration Ready
**Document Version:** 2026-03-19
**Applicable to:** Phase 9 and later
