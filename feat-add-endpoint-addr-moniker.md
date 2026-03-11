# feat-add-endpoint-addr-moniker

## Objectif

1. Créer un endpoint `GET /addr_moniker?addr=...` pour résoudre un moniker depuis une adresse.
2. Modifier `valoper.go` pour peupler la table `addr_monikers` (upsert) lors de `InitMonikerMap`.
3. Préparer la suppression de la colonne `moniker` de `daily_participations` (normalisation).

---

## Analyse d'impact

### Niveau de risque : ÉLEVÉ

La colonne `moniker` est présente à **6 couches** du système :

| Couche | Nb de points touchés |
| --- | --- |
| Insertion dans `daily_participations` | 5 fonctions |
| Requêtes métriques SQL | 6 requêtes |
| Vue SQLite `daily_missing_series` | 1 vue (critique) |
| Détection d'alertes | 2 fonctions |
| Lookups Telegram | 3 fonctions |
| Structs de réponse API/bot | 6 structs |

---

## Phase 1 — Peupler `addr_monikers` depuis `valoper.go`

### Fichier : `backend/internal/gnovalidator/valoper.go`

#### Changement : fin de `InitMonikerMap()` (après la boucle qui construit `MonikerMap`)

Après la boucle finale qui remplit `MonikerMap`, ajouter un upsert vers `addr_monikers` :

```go
// Après avoir construit MonikerMap, persister dans addr_monikers
for addr, moniker := range MonikerMap {
    if err := database.UpsertAddrMoniker(db, addr, moniker); err != nil {
        log.Printf("⚠️ Failed to upsert addr_moniker %s: %v", addr, err)
    }
}
log.Printf("✅ addr_monikers table synced (%d entries)", len(MonikerMap))
```

### Fichier : `backend/internal/database/db.go`

#### Nouvelle fonction à ajouter

```go
func UpsertAddrMoniker(db *gorm.DB, addr, moniker string) error {
    return db.Exec(`
        INSERT INTO addr_monikers (addr, moniker)
        VALUES (?, ?)
        ON CONFLICT(addr) DO UPDATE SET moniker = excluded.moniker
    `, addr, moniker).Error
}
```

### Modèle `AddrMoniker` dans `db_init.go`

Vérifier que le modèle a bien une contrainte unique sur `addr` :

```go
type AddrMoniker struct {
    Addr    string `gorm:"primaryKey"`
    Moniker string
}
```

Si `Addr` n'est pas `primaryKey` ou n'a pas d'index unique, la migration suivante est nécessaire :

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_addr_monikers_addr ON addr_monikers(addr);
```

---

## Phase 2 — Nouvel endpoint API `GET /addr_moniker`

### Fichier : `backend/internal/database/db.go` (lookup)

#### Nouvelle fonction de lookup

```go
func GetMonikerByAddr(db *gorm.DB, addr string) (string, error) {
    var result struct{ Moniker string }
    err := db.Table("addr_monikers").
        Select("moniker").
        Where("addr = ?", addr).
        First(&result).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return "", nil
    }
    return result.Moniker, err
}
```

### Fichier : `backend/internal/api/api.go`

#### Nouveau handler

```go
func GetAddrMonikerHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
    EnableCORS(w)
    if r.Method != http.MethodGet {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    addr := r.URL.Query().Get("addr")
    if addr == "" {
        http.Error(w, "Missing addr parameter", http.StatusBadRequest)
        return
    }
    moniker, err := database.GetMonikerByAddr(db, addr)
    if err != nil {
        http.Error(w, fmt.Sprintf("Failed to get moniker: %v", err), http.StatusInternalServerError)
        return
    }
    if moniker == "" {
        http.Error(w, "Address not found", http.StatusNotFound)
        return
    }
    json.NewEncoder(w).Encode(map[string]string{"addr": addr, "moniker": moniker})
}
```

#### Enregistrement dans `StartWebhookAPI()`

```go
mux.HandleFunc("/addr_moniker", func(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        GetAddrMonikerHandler(w, r, db)
    case http.MethodOptions:
        EnableCORS(w)
        w.WriteHeader(http.StatusOK)
    default:
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
    }
})
```

Endpoint public, pas de middleware Clerk (cohérent avec `/uptime`, `/block_height`, etc.).

---

## Phase 3 — Suppression de `moniker` de `daily_participations`

### 3.1 — Requête de migration de données

Avant de supprimer la colonne, rétro-peupler `addr_monikers` depuis les données existantes :

```sql
-- Peupler addr_monikers depuis daily_participations (données historiques)
INSERT INTO addr_monikers (addr, moniker)
SELECT DISTINCT addr, moniker
FROM daily_participations
WHERE moniker IS NOT NULL AND moniker != '' AND moniker != 'unknown'
ON CONFLICT(addr) DO UPDATE SET moniker = excluded.moniker;

-- Vérifier le résultat
SELECT COUNT(*) FROM addr_monikers;
```

### 3.2 — Suppression de la colonne (SQLite)

SQLite ne supporte pas `ALTER TABLE DROP COLUMN` avant la version 3.35. La migration passe par une recréation de table :

```sql
-- Étape 1 : Créer la nouvelle table sans moniker
CREATE TABLE daily_participations_new (
    date            DATETIME,
    block_height    INTEGER,
    addr            TEXT,
    participated    BOOLEAN,
    tx_contribution BOOLEAN,
    PRIMARY KEY (block_height, addr)
);

-- Étape 2 : Copier les données
INSERT INTO daily_participations_new (date, block_height, addr, participated, tx_contribution)
SELECT date, block_height, addr, participated, tx_contribution
FROM daily_participations;

-- Étape 3 : Supprimer l'ancienne table
DROP TABLE daily_participations;

-- Étape 4 : Renommer
ALTER TABLE daily_participations_new RENAME TO daily_participations;

-- Étape 5 : Recréer les index si nécessaire
CREATE INDEX IF NOT EXISTS idx_dp_addr ON daily_participations(addr);
CREATE INDEX IF NOT EXISTS idx_dp_date ON daily_participations(date);
```

> ⚠️ Cette migration doit être effectuée hors production, avec backup préalable (`cp db/webhooks.db db/webhooks.db.bak`).

---

## Phase 4 — Modifications SQL des requêtes métriques

Toutes les requêtes suivantes doivent remplacer `dp.moniker` ou `moniker` par un `LEFT JOIN addr_monikers`.

### Pattern de JOIN à utiliser

```sql
LEFT JOIN addr_monikers am ON am.addr = dp.addr
```

Et remplacer `moniker` dans SELECT/GROUP BY par `COALESCE(am.moniker, dp.addr) AS moniker`.

---

### 4.1 — `GetCurrentPeriodParticipationRate()` — `db_metrics.go`

**Avant :**

```sql
SELECT addr, moniker,
    ROUND(SUM(participated) * 100.0 / COUNT(*), 1) AS participation_rate
FROM daily_participations
WHERE date >= %s AND date < %s
GROUP BY addr, moniker
ORDER BY participation_rate ASC
```

**Après :**

```sql
SELECT dp.addr,
    COALESCE(am.moniker, dp.addr) AS moniker,
    ROUND(SUM(dp.participated) * 100.0 / COUNT(*), 1) AS participation_rate
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE dp.date >= %s AND dp.date < %s
GROUP BY dp.addr
ORDER BY participation_rate ASC
```

---

### 4.2 — `UptimeMetricsaddr()` — `db_metrics.go`

**Après :**

```sql
WITH bounds AS (...),
base AS (
    SELECT
        p.addr,
        SUM(CASE WHEN p.participated THEN 1 ELSE 0 END) AS ok,
        COUNT(*) AS total
    FROM daily_participations p
    JOIN bounds b ON p.block_height BETWEEN b.start_h AND b.end_h
    GROUP BY p.addr
)
SELECT
    COALESCE(am.moniker, base.addr) AS moniker,
    base.addr,
    100.0 * ok / total AS uptime
FROM base
LEFT JOIN addr_monikers am ON am.addr = base.addr
ORDER BY uptime ASC
```

---

### 4.3 — `OperationTimeMetricsaddr()` — `db_metrics.go`

**Après :**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    MAX(dp.date) AS last_down_date,
    (SELECT MAX(date) FROM daily_participations d2
     WHERE d2.addr = dp.addr AND d2.participated = 1) AS last_up_date,
    ROUND(...) AS days_diff
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE dp.participated = 0
GROUP BY dp.addr
```

---

### 4.4 — `TxContrib()` — `db_metrics.go`

**Après :**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    ROUND((SUM(dp.tx_contribution) * 100.0 / ...), 1) AS tx_contrib
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE dp.date >= %s AND dp.date < %s
GROUP BY dp.addr
```

---

### 4.5 — `MissingBlock()` — `db_metrics.go`

**Après :**

```sql
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END) AS missing_block
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE dp.date >= %s AND dp.date < %s
GROUP BY dp.addr
```

---

### 4.6 — `CalculateRate()` — `gnovalidator_report.go`

**Après :**

```sql
SELECT
    dp.addr,
    COALESCE(am.moniker, dp.addr) AS moniker,
    COUNT(*) AS total_blocks,
    SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) AS participated_blocks
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
WHERE date(dp.date) = ?
GROUP BY dp.addr
```

---

## Phase 5 — Réécriture de la vue `daily_missing_series`

**Fichier : `backend/internal/database/db.go` — `CreateMissingBlocksView()`**

C'est le changement le plus critique car cette vue alimente le système d'alertes en temps réel.

**Après :**

```sql
CREATE VIEW IF NOT EXISTS daily_missing_series AS
WITH ranked AS (
    SELECT
        dp.addr,
        COALESCE(am.moniker, dp.addr) AS moniker,
        dp.date,
        dp.block_height,
        dp.participated,
        CASE
            WHEN dp.participated = 0 AND LAG(dp.participated) OVER
                (PARTITION BY dp.addr, DATE(dp.date) ORDER BY dp.block_height) = 1
            THEN 1
            WHEN dp.participated = 0 AND LAG(dp.participated) OVER
                (PARTITION BY dp.addr, DATE(dp.date) ORDER BY dp.block_height) IS NULL
            THEN 1
            ELSE 0
        END AS new_seq
    FROM daily_participations dp
    LEFT JOIN addr_monikers am ON am.addr = dp.addr
    WHERE dp.date >= datetime('now', '-24 hours')
),
grouped AS (
    SELECT *,
        SUM(new_seq) OVER (PARTITION BY addr, DATE(date) ORDER BY block_height) AS seq_id
    FROM ranked
)
SELECT
    addr,
    moniker,
    DATE(date) AS date,
    TIME(date) AS time_block,
    MIN(block_height) OVER (PARTITION BY addr, DATE(date), seq_id) AS start_height,
    block_height AS end_height,
    SUM(1) OVER (
        PARTITION BY addr, DATE(date), seq_id
        ORDER BY block_height
        ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
    ) AS missed
FROM grouped
WHERE participated = 0
ORDER BY addr, date, seq_id, block_height;
```

> ⚠️ La vue doit être `DROP`pée puis recrée au démarrage. Modifier `CreateMissingBlocksView()` pour faire un `DROP VIEW IF EXISTS daily_missing_series` avant le `CREATE VIEW`.

---

## Phase 6 — Requêtes Telegram (`db_telegram.go`)

### `GetValidatorStatusList()`

**Après :**

```sql
WITH v AS (
    SELECT DISTINCT dp.addr, COALESCE(am.moniker, dp.addr) AS moniker
    FROM daily_participations dp
    LEFT JOIN addr_monikers am ON am.addr = dp.addr
)
SELECT v.moniker, v.addr,
    CASE WHEN s.activate = 1 THEN 'on' ELSE 'off' END AS status
FROM v
LEFT JOIN telegram_validator_subs s ON s.addr = v.addr AND s.chat_id = ?
ORDER BY status DESC
```

### `GetAllValidators()`

**Après :**

```sql
SELECT DISTINCT am.addr, COALESCE(am.moniker, dp.addr) AS moniker
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.addr = dp.addr
```

### `ResolveAddrs()`

**Après :**

```go
err := db.Raw(`
    SELECT am.addr, COALESCE(am.moniker, am.addr) AS moniker
    FROM addr_monikers am
    WHERE am.addr IN ?
`, addrs).Scan(&results).Error
```

---

## Phase 7 — Suppressions dans le code d'insertion

### `gnovalidator_realtime.go` — `SaveParticipation()`

```go
// Avant
stmt := `INSERT OR REPLACE INTO daily_participations
    (date, block_height, moniker, addr, participated, tx_contribution)
    VALUES (?, ?, ?, ?, ?, ?)`
tx.Exec(stmt, timeStp, blockHeight, moniker, valAddr, ...)

// Après
stmt := `INSERT OR REPLACE INTO daily_participations
    (date, block_height, addr, participated, tx_contribution)
    VALUES (?, ?, ?, ?, ?)`
tx.Exec(stmt, timeStp, blockHeight, valAddr, ...)
```

### `sync.go` — `dpRow` struct + `flushChunk()`

```go
// Supprimer le champ Moniker du struct dpRow
type dpRow struct {
    Date           time.Time
    BlockHeight    int64
    // Moniker supprimé
    Addr           string
    Participated   bool
    TxContribution bool
}

// flushChunk() : retirer moniker du INSERT et du ON CONFLICT UPDATE
q := `INSERT INTO daily_participations
    (date, block_height, addr, participated, tx_contribution)
    VALUES `
// ON CONFLICT : retirer "moniker = excluded.moniker"
```

### `db_init.go` — `DailyParticipation` struct

```go
type DailyParticipation struct {
    Date           time.Time
    BlockHeight    int64
    // Moniker string   ← SUPPRIMER
    Addr           string
    Participated   bool
    TxContribution bool
}
```

---

## Récapitulatif des fichiers à modifier

| Fichier | Modifications | Priorité |
| --- | --- | --- |
| `database/db.go` | `UpsertAddrMoniker()`, `GetMonikerByAddr()`, réécriture vue `daily_missing_series` | **Critique** |
| `database/db_init.go` | Retirer `Moniker` de `DailyParticipation`, index unique sur `addr_monikers` | **Critique** |
| `database/db_metrics.go` | 5 requêtes SQL avec JOIN `addr_monikers` | **Critique** |
| `gnovalidator/valoper.go` | Appel `UpsertAddrMoniker` à la fin de `InitMonikerMap` | **Critique** |
| `gnovalidator/gnovalidator_realtime.go` | Retirer `moniker` des INSERT, `WatchValidatorAlerts`, `SendResolveAlerts` | **Critique** |
| `gnovalidator/sync.go` | Retirer `Moniker` de `dpRow`, `flushChunk`, `BackfillRange`, `BackfillParallel` | **Critique** |
| `gnovalidator/gnovalidator_report.go` | Réécrire `CalculateRate()` avec JOIN | Élevée |
| `database/db_telegram.go` | 3 requêtes avec JOIN `addr_monikers` | Élevée |
| `api/api.go` | Nouveau handler `GetAddrMonikerHandler` + route `/addr_moniker` | Moyenne |
| `gnovalidator_report_test.go` | Adapter les fixtures de test | Faible |

## Fichiers sans modification nécessaire

- `telegram/validator.go` — les formatters reçoivent des structs déjà résolus, pas d'accès direct à la DB
- `telegram/telegram.go` — idem
- `gnovalidator/Prometheus.go` — pas d'accès à `daily_participations`
- `internal/fonction.go` — reçoit moniker en paramètre

---

## Ordre d'exécution recommandé

1. **Ajouter `UpsertAddrMoniker`** dans `db.go` et l'appeler depuis `valoper.go` → peupler la table sans rien casser.
2. **Ajouter le nouvel endpoint** `/addr_moniker` → testable immédiatement après l'étape 1.
3. **Réécrire les 5 requêtes métriques** avec JOIN → les tester en parallèle de l'ancien code.
4. **Réécrire la vue** `daily_missing_series` → tester le système d'alertes en staging.
5. **Réécrire les 3 requêtes Telegram** dans `db_telegram.go`.
6. **Réécrire `CalculateRate()`** dans `gnovalidator_report.go`.
7. **Exécuter la migration SQL** (backup → INSERT INTO addr_monikers → recréation table).
8. **Supprimer `Moniker`** de `DailyParticipation`, `dpRow`, tous les INSERT.
9. **Lancer `go build ./...` et les tests**.
