# Migration Multi-Chain - Guide Complet

**Date:** 2026-03-19
**Version:** Phase 9 Complete
**Scope:** Production migration guide from single-chain to multi-chain

---

## 📋 Résumé des Modifications de Schéma

### Tables Modifiées (14 total)

| Table | Colonnes Ajoutées | Migrations Required | Impact |
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

## 🔄 Étapes de Migration EN PRODUCTION

### Phase 1: Préparation (AVANT le déploiement)

```bash
# 1. Sauvegarder la DB actuelle
cp backend/db/webhooks.db backend/db/webhooks.db.backup-2026-03-19

# 2. Vérifier le schéma existant
sqlite3 backend/db/webhooks.db ".tables"
sqlite3 backend/db/webhooks.db ".schema daily_participations"

# 3. Déterminer la chaîne par défaut ACTUELLE
# Si votre deployment actuel utilisait une seule chaîne:
# - Betanet → default_chain: "betanet"
# - Gnoland1 → default_chain: "gnoland1"
```

### Phase 2: Déployer la nouvelle version du code

```bash
# 1. Mettre à jour config.yaml
# OLD:
# rpc_endpoint: "https://rpc.betanet.gno.land"
# graphql: "https://indexer.betanet.gno.land/graphql/query"
# gnoweb: "https://betanet.gno.land"

# NEW:
# default_chain: "betanet"  # IMPORTANT: À ajuster selon votre setup
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
#     enabled: false  # Activer si besoin

# 2. Arrêter l'application
systemctl stop gnomonitoring  # ou votre script de démarrage

# 3. Déployer le nouveau binaire
go build -o /usr/local/bin/gnomonitoring ./backend

# 4. Démarrer l'application (elle appliquera les migrations)
systemctl start gnomonitoring
```

### Phase 3: Vérifier les migrations

```bash
# 1. Vérifier que chain_id a été ajouté à daily_participations
sqlite3 backend/db/webhooks.db ".schema daily_participations"
# Vous devriez voir: chain_id TEXT NOT NULL DEFAULT 'betanet'

# 2. Vérifier les données
sqlite3 backend/db/webhooks.db "SELECT DISTINCT chain_id FROM daily_participations LIMIT 5;"
# Doit retourner: betanet (ou votre chaîne par défaut)

# 3. Vérifier que les indexes ont été créés
sqlite3 backend/db/webhooks.db "SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'idx_dp_%';"
```

---

## 🗺️ Tables Détaillées et Migrations

### 1. CRITICAL: `daily_participations`

**Ancienne structure:**
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

**Nouvelle structure:**
```sql
ALTER TABLE daily_participations ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
-- New Unique Index: (chain_id, addr, block_height)
-- Old Index: (addr, block_height) — À SUPPRIMER
```

**Migration SQL (si manuel):**
```sql
-- 1. Ajouter la colonne
ALTER TABLE daily_participations ADD COLUMN chain_id TEXT DEFAULT 'betanet';

-- 2. Mettre à jour les données (si besoin)
UPDATE daily_participations SET chain_id = 'betanet' WHERE chain_id IS NULL;

-- 3. Rendre NOT NULL
ALTER TABLE daily_participations MODIFY chain_id TEXT NOT NULL;

-- 4. Créer les nouveaux indexes
CREATE INDEX idx_dp_chain_block_height ON daily_participations(chain_id, block_height);
CREATE INDEX idx_dp_chain_addr ON daily_participations(chain_id, addr);
CREATE INDEX idx_dp_chain_date ON daily_participations(chain_id, date);
CREATE INDEX idx_dp_chain_addr_participated ON daily_participations(chain_id, addr, participated);

-- 5. Créer la nouvelle unique constraint
CREATE UNIQUE INDEX uniq_chain_addr_height ON daily_participations(chain_id, addr, block_height);

-- 6. Supprimer l'ancien index (SQLite ne supporte pas les contraintes)
-- Note: SQLite ne permet pas de supprimer les índices automatiques
```

**Impact:**
- ✅ Zéro perte de données
- ✅ Toutes les lignes existantes → `chain_id = 'betanet'`
- ✅ Backward compatible (si on garde `chain_id = NULL`)

---

### 2. CRITICAL: `alert_logs`

**Migration SQL:**
```sql
ALTER TABLE alert_logs ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
CREATE INDEX idx_al_chain_addr ON alert_logs(chain_id, addr);
```

**Impact:**
- ✅ Zéro perte de données
- ✅ Nouvelles alertes seront correctement scoped par chaîne

---

### 3. CRITICAL: `addr_monikers`

**Migration SQL:**
```sql
ALTER TABLE addr_monikers ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
CREATE UNIQUE INDEX uniq_chain_addr ON addr_monikers(chain_id, addr);
```

**Impact:**
- ✅ Chaque chaîne peut avoir sa propre version du moniker pour une adresse
- ⚠️ Les monikers existants seront associés à 'betanet'

---

### 4. CRITICAL: `govdaos`

**Migration SQL:**
```sql
ALTER TABLE govdaos ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
```

**Impact:**
- ✅ Proposals à venir seront scoped par chaîne
- ✅ Historique existant restera lié à betanet

---

### 5. HIGH: `telegrams`

**Migration SQL:**
```sql
ALTER TABLE telegrams ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
```

**Impact:**
- ✅ Chaque chat Telegram peut maintenant avoir des préférences différentes par chaîne
- ✅ Les préférences existantes seront associées à betanet

---

### 6. HIGH: `telegram_hour_reports`

**Migration SQL:**
```sql
ALTER TABLE telegram_hour_reports ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
```

**Impact:**
- ✅ Chaque chat peut recevoir des rapports pour plusieurs chaînes
- ✅ Les rapports existants continueront de fonctionner

---

### 7. HIGH: `telegram_validator_subs`

**Migration SQL:**
```sql
ALTER TABLE telegram_validator_subs ADD COLUMN chain_id TEXT NOT NULL DEFAULT 'betanet';
CREATE UNIQUE INDEX idx_tvs_chain_addr_chatid ON telegram_validator_subs(chain_id, addr, chat_id);
```

**Impact:**
- ✅ Chaque chat peut souscrire au même validateur sur plusieurs chaînes
- ⚠️ Les subscriptions existantes seront liées à betanet

---

### 8. MEDIUM: `webhook_validators` & `webhook_gov_daos`

**Migration SQL:**
```sql
ALTER TABLE webhook_validators ADD COLUMN chain_id TEXT DEFAULT NULL;
ALTER TABLE webhook_gov_daos ADD COLUMN chain_id TEXT DEFAULT NULL;
```

**Impact:**
- ✅ `chain_id = NULL` → reçoit les alertes de TOUTES les chaînes
- ✅ `chain_id = 'betanet'` → reçoit les alertes de betanet uniquement
- ✅ Backward compatible (NULL = comportement ancien)

---

## ⚠️ Points Critiques à Vérifier

### 1. Vérifier `config.yaml` AVANT la migration

```yaml
# ❌ ANCIEN (va CASSER):
rpc_endpoint: "https://rpc.betanet.gno.land"
graphql: "https://indexer.betanet.gno.land/graphql/query"
gnoweb: "https://betanet.gno.land"

# ✅ NOUVEAU (requis):
default_chain: "betanet"
chains:
  betanet:
    rpc_endpoint: "https://rpc.betanet.gno.land"
    graphql: "https://indexer.betanet.gno.land/graphql/query"
    gnoweb: "https://betanet.gno.land"
    enabled: true
```

### 2. Déterminer le `default_chain` CORRECT

Votre deployment actuel utilise quelle chaîne? Sélectionnez le nom exact:

```bash
# Déterminer automatiquement
curl -s $(cat /path/to/config.yaml | grep rpc_endpoint | cut -d'"' -f2) /status
# Indiquez le chaîne dans les logs

# Ou vérifier manuellement la DB
sqlite3 backend/db/webhooks.db "SELECT COUNT(*) as proposal_count FROM govdaos;"
# Beaucoup de proposals → C'est probablement votre chaîne actuelle
```

### 3. Vérifier les Webhooks

```bash
# Compter les webhooks existants
sqlite3 backend/db/webhooks.db "SELECT 'validators' as type, COUNT(*) FROM webhook_validators UNION SELECT 'govdao' as type, COUNT(*) FROM webhook_gov_daos;"

# Résultat: Après migration, tous auront chain_id = NULL (reçoivent toutes les alertes)
# C'est BACKWARD COMPATIBLE
```

---

## 🔙 ROLLBACK: Revenir à Single-Chain

Si vous devez revenir à la version single-chain:

### Option 1: Restaurer depuis la backup

```bash
# 1. Arrêter l'application
systemctl stop gnomonitoring

# 2. Restaurer la backup
cp backend/db/webhooks.db.backup-2026-03-19 backend/db/webhooks.db

# 3. Revenir au code ancien
git checkout main -- backend/
go build -o /usr/local/bin/gnomonitoring ./backend

# 4. Redémarrer
systemctl start gnomonitoring
```

### Option 2: Supprimer les colonnes `chain_id` (DÉCONSEILLÉ)

```bash
# ⚠️ TRÈS DESTRUCTIF - Les données de chaîne seront perdues

# Pour SQLite, vous devez recréer les tables (pas d'ALTER TABLE DROP COLUMN)
-- Cela est complexe et risqué. Utilisez l'Option 1 à la place.
```

---

## 📊 Checklist de Migration

### PRE-MIGRATION
- [ ] Sauvegarder `webhooks.db`
- [ ] Vérifier `config.yaml` structure (ancien format)
- [ ] Déterminer `default_chain` à utiliser
- [ ] Tester la nouvelle config.yaml en DEV
- [ ] Vérifier les webhooks existants (compte)
- [ ] Vérifier les Telegram chats existants (compte)

### MIGRATION
- [ ] Arrêter gnomonitoring
- [ ] Mettre à jour `config.yaml`
- [ ] Déployer le nouveau binaire
- [ ] Démarrer gnomonitoring (migrations appliquées automatiquement)
- [ ] Vérifier les logs pour erreurs

### POST-MIGRATION
- [ ] Vérifier `chain_id` a été ajouté partout
- [ ] Tester l'API: `GET /Participation?chain=betanet&address=...`
- [ ] Tester Telegram: `/chain` command
- [ ] Tester GovDAO: `/chain` command
- [ ] Vérifier les webhooks reçoivent les alertes
- [ ] Vérifier les rapports Telegram fonctionnent
- [ ] Archiver la backup: `webhooks.db.backup-2026-03-19`

---

## 🎯 Tableau Récapitulatif des Changements

### Configuration

| Élément | Ancien | Nouveau | Action |
|---------|--------|---------|--------|
| Format config | Flat (rpc_endpoint, graphql) | Hierarchical (chains) | Rewrite config.yaml |
| Nombres de chaînes | 1 | N (enabled: true) | Ajouter nouvelles chaînes |
| Default chain | Implicite (première) | Explicite (default_chain) | Spécifier dans config |

### Base de Données

| Table | Avant | Après | Données | Migration |
|-------|-------|-------|---------|-----------|
| daily_participations | sans chain_id | avec chain_id | Set à 'betanet' | ALTER TABLE |
| alert_logs | sans chain_id | avec chain_id | Set à 'betanet' | ALTER TABLE |
| addr_monikers | sans chain_id | avec chain_id | Set à 'betanet' | ALTER TABLE |
| telegram_* | sans chain_id | avec chain_id | Set à 'betanet' | ALTER TABLE |
| webhooks_* | sans chain_id | avec chain_id (NULL) | Unchanged | ALTER TABLE |

### API

| Endpoint | Avant | Après | Change |
|----------|-------|-------|--------|
| GET /Participation | Global | ?chain=betanet | Optional param |
| GET /uptime | Global | ?chain=betanet | Optional param |
| POST /webhooks | Global scope | chain_id (optional) | Chain scoping |
| GET /info | Single endpoints | Array of chains | New format |

### Telegram

| Commande | Avant | Après | Comportement |
|----------|-------|-------|---|
| /subscribe | Single chain | Per-chat choice | Utilise /setchain |
| /status | Single chain | Per-chat choice | Utilise /setchain |
| /chain | N/A | List chains | NOUVEAU |
| /setchain | N/A | Switch chain | NOUVEAU |

---

## 📝 Commandes Utiles de Vérification

```bash
# 1. Vérifier la DB après migration
sqlite3 backend/db/webhooks.db "PRAGMA table_info(daily_participations);"

# 2. Compter les lignes par chaîne
sqlite3 backend/db/webhooks.db "SELECT chain_id, COUNT(*) FROM daily_participations GROUP BY chain_id;"

# 3. Vérifier les indexes
sqlite3 backend/db/webhooks.db ".indices"

# 4. Vérifier la taille de la DB
ls -lh backend/db/webhooks.db

# 5. Vérifier les webhooks
sqlite3 backend/db/webhooks.db "SELECT type, COUNT(*), COUNT(CASE WHEN chain_id IS NOT NULL THEN 1 END) FROM webhook_validators GROUP BY type;"
```

---

## 🚨 Erreurs Courantes et Solutions

### Erreur 1: `column chain_id already exists`
**Cause:** La colonne a déjà été ajoutée
**Solution:** Vérifier le schéma, la migration est idempotente

### Erreur 2: `config.yaml: missing field rpc_endpoint`
**Cause:** Format config non mis à jour
**Solution:** Utiliser le format hierarchique avec `chains:`

### Erreur 3: Webhooks ne reçoivent rien
**Cause:** chain_id = NULL (correct) mais handlers ne filtrent pas
**Solution:** Redémarrer l'application, vérifier les logs

### Erreur 4: Telegram commands /chain, /setchain introuvables
**Cause:** Vieux binaire en mémoire ou pas compilé
**Solution:** `go build ./...` + `systemctl restart gnomonitoring`

---

## 📞 Support et Questions

Pour les questions sur la migration:
1. Vérifier que `config.yaml` est correct
2. Vérifier que `default_chain` corresponds à votre setup
3. Consulter les logs: `journalctl -u gnomonitoring -f`
4. Utiliser les commandes de vérification ci-dessus

---

**Status:** ✅ Migration Multi-Chain Ready
**Document Version:** 2026-03-19
**Applicable to:** Phase 9 and later
