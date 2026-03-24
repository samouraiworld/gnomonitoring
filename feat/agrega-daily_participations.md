# feat: Aggregation of daily_participations

## Motivation

La table `daily_participations` stocke **une ligne par (chain_id, addr, block_height)**.
Sur une chaîne avec 100 validateurs et ~720 blocs/heure, cela représente ~1.7M lignes/jour.
Les requêtes de métriques (Prometheus, Telegram, API) agrègent quasi-systématiquement par jour,
mais doivent scanner des millions de lignes brutes pour y arriver.

L'objectif est de rester sur **SQLite** (pas de migration vers PostgreSQL/MySQL) en ajoutant une table
agrégée `daily_participation_agrega` qui pré-calcule les statistiques quotidiennes par validateur.

---

## Stratégie : double table avec fenêtre glissante de données brutes

| Couche | Table | Granularité | Rôle |
|---|---|---|---|
| Données brutes récentes | `daily_participations` | 1 ligne / block | Alertes temps réel, streak detection |
| Données agrégées historiques | `daily_participation_agrega` | 1 ligne / (addr, jour) | Métriques mois en cours, 30 jours, uptime, TX contrib... |

**Fenêtre de rétention des données brutes** : 2 jours minimum, 7 jours recommandé par sécurité.

Le seul besoin en données brutes est lié aux **alertes** :
- `WatchValidatorAlerts` / `SendResolveAlerts` : fenêtre de **24h**
- `GetMissedBlocksWindow` (1h) : fenêtre de **1h**

Les métriques "100 derniers blocs" (`GetActiveValidatorCount`, `GetAvgParticipationRate`) et
"200 derniers blocs" (`CalculateConsecutiveMissedBlocks`) sont en réalité l'équivalent de
**quelques minutes à quelques heures** de blocs — elles sont toujours couvertes par la rétention de 2 jours.

7 jours offre une marge confortable pour absorber un éventuel retard du job d'agrégation.
Au-delà, les lignes brutes sont purgées après agrégation.

---

## Proposition de table `daily_participation_agrega`

```sql
CREATE TABLE IF NOT EXISTS daily_participation_agrega (
    chain_id               TEXT     NOT NULL,
    addr                   TEXT     NOT NULL,
    block_date             DATE     NOT NULL,  -- YYYY-MM-DD (jour UTC)
    moniker                TEXT,
    participated_count     INTEGER  NOT NULL,  -- SUM(participated)
    missed_count           INTEGER  NOT NULL,  -- SUM(1 - participated) == total_blocks - participated_count
    tx_contribution_count  INTEGER  NOT NULL,  -- SUM(tx_contribution)
    total_blocks           INTEGER  NOT NULL,  -- COUNT(*) sur la journée
    first_block_height     INTEGER  NOT NULL,  -- MIN(block_height) du jour
    last_block_height      INTEGER  NOT NULL,  -- MAX(block_height) du jour
    PRIMARY KEY (chain_id, addr, block_date)
);

-- Index pour les requêtes par plage de dates sur une chaîne
CREATE INDEX IF NOT EXISTS idx_dpa_chain_date      ON daily_participation_agrega(chain_id, block_date);
-- Index couvrant pour les requêtes par validateur sur une chaîne
CREATE INDEX IF NOT EXISTS idx_dpa_chain_addr_date ON daily_participation_agrega(chain_id, addr, block_date);
```

### Justification des colonnes

| Colonne | Pourquoi |
|---|---|
| `participated_count` | Remplace `SUM(participated)` sur les requêtes de taux (uptime, participation rate) |
| `missed_count` | Remplace `SUM(CASE WHEN participated = 0 THEN 1 ELSE 0 END)` (missing blocks month, window) |
| `tx_contribution_count` | Remplace `SUM(tx_contribution)` (TX contrib metric) |
| `total_blocks` | Dénominateur pour les calculs de taux — évite un `COUNT(*)` |
| `first_block_height` / `last_block_height` | Permet de reconstruire la continuité des blocs pour `operation_time` et `first_seen` |

---

## Classement des requêtes existantes

### Requêtes pouvant migrer vers `daily_participation_agrega`

| Fonction | Fenêtre actuelle | Requête agrégée équivalente |
|---|---|---|
| `GetCurrentPeriodParticipationRate` | Mois en cours | `SUM(participated_count) * 100.0 / SUM(total_blocks)` GROUP BY addr WHERE block_date IN mois |
| `UptimeMetricsaddr` | 30 derniers jours | `SUM(participated_count) * 100.0 / SUM(total_blocks)` WHERE block_date >= NOW()-30d |
| `TxContrib` | Mois en cours | `SUM(tx_contribution_count) * 100.0 / NULLIF(total_tx, 0)` |
| `MissingBlock` | Mois en cours | `SUM(missed_count)` WHERE block_date IN mois |
| `GetMissedBlocksWindow` (7d) | 7 jours | `SUM(missed_count)` WHERE block_date >= NOW()-7d |
| `GetFirstSeen` | Tout l'historique | `MIN(block_date)` WHERE participated_count > 0 (approx. du MIN(date) exact) |
| `OperationTimeMetricsaddr` | Tout l'historique | `MAX(block_date) WHERE missed_count > 0` et `MAX(block_date) WHERE participated_count > 0` |
| `CalculateRate` (rapport quotidien) | Jour spécifique | SELECT direct sur la ligne du jour concerné |
| `CalculateMissedBlocks` (aujourd'hui) | Aujourd'hui | `missed_count` WHERE block_date = TODAY |

### Requêtes devant rester sur `daily_participations` (données brutes)

Ces requêtes nécessitent la granularité au niveau du bloc :

| Fonction | Raison |
|---|---|
| `CalculateConsecutiveMissedBlocks` | Calcul de streak in-memory bloc par bloc, ORDER BY block_height |
| `WatchValidatorAlerts` | Vue `daily_missing_series` avec `LAG()` sur block_height consécutifs |
| `SendResolveAlerts` | Vérifie `participated` au block `end_height + 1` précis |
| `GetAvgParticipationRate` | Moyenne sur les 100 derniers blocs (granularité bloc) |
| `GetActiveValidatorCount` | DISTINCT addr sur les 100 derniers blocs |
| `GetCurrentChainHeight` | MAX(block_height) - pas de date |

### Requêtes avec fenêtre courte à conserver sur les données brutes

| Fonction | Fenêtre | Note |
|---|---|---|
| `GetMissedBlocksWindow` (1h) | 1 heure | Trop court pour une agrégation journalière, garder sur données brutes |
| `GetMissedBlocksWindow` (24h) | 24h | À la limite — OK sur données brutes avec la rétention 7j |

---

## Changements requis par composant

### 1. `internal/database/db_init.go`

- Ajouter le `CREATE TABLE IF NOT EXISTS daily_participation_agrega` et ses index.
- Ajouter une migration idempotente (vérification via `pragma_table_info`) pour les DBs existantes.
- Ajouter la politique de purge : supprimer les lignes `daily_participations` de plus de 7 jours
  (peut être fait dans `InitDB` ou dans le job d'agrégation lui-même).

### 2. Nouveau job d'agrégation : `internal/gnovalidator/aggregator.go`

Logique :
1. Pour chaque chain activée, trouver le dernier `block_date` présent dans `daily_participation_agrega`.
2. Agréger les jours complets non encore agrégés (jours `< today`) depuis `daily_participations`.
3. Faire un UPSERT dans `daily_participation_agrega` (ON CONFLICT(chain_id, addr, block_date) DO UPDATE).
4. Supprimer les lignes `daily_participations` plus vieilles que 7 jours.

Le job tourne :
- **Au démarrage** (rattrapage de tout l'historique non encore agrégé).
- **Une fois par heure** (ou à minuit UTC pour agréger la journée précédente) via une goroutine dans `main.go`.

```go
func StartAggregator(db *gorm.DB) {
    runAggregation(db) // rattrapage au démarrage
    ticker := time.NewTicker(1 * time.Hour)
    for range ticker.C {
        runAggregation(db)
    }
}

func runAggregation(db *gorm.DB) {
    for _, chainID := range internal.EnabledChains {
        aggregateChain(db, chainID)
        pruneRawData(db, chainID, 7*24*time.Hour)
    }
}
```

> **Note sur le sync au démarrage** : si une nouvelle chaîne est ajoutée, `runAggregation` au démarrage
> traitera tout l'historique backfillé de cette chaîne automatiquement — aucune logique spéciale n'est
> nécessaire dans `sync.go`. Il suffit de lancer `StartAggregator` après `BackfillParallel`.

### 3. `internal/database/db_metrics.go`

Réécrire les fonctions listées dans "Requêtes pouvant migrer" pour cibler `daily_participation_agrega`.
Exemple pour `UptimeMetricsaddr` :

```sql
-- Avant (sur daily_participations)
SELECT addr, 100.0 * SUM(CASE WHEN participated THEN 1 ELSE 0 END) / COUNT(*) AS uptime
FROM daily_participations
WHERE chain_id = ? AND date >= date('now', '-30 days')
GROUP BY addr

-- Après (sur daily_participation_agrega)
SELECT addr, 100.0 * SUM(participated_count) / SUM(total_blocks) AS uptime
FROM daily_participation_agrega
WHERE chain_id = ? AND block_date >= date('now', '-30 days')
GROUP BY addr
```

### 4. `internal/gnovalidator/gnovalidator_report.go`

`CalculateRate(date)` : remplacer la requête par un SELECT direct sur `daily_participation_agrega`
où `block_date = date`.

### 5. `internal/gnovalidator/metric.go`

`CalculateMissedBlocks()` : si la date cible est `>= yesterday`, garder sur `daily_participations`.
Si la date est ancienne (edge case rapport manuel), utiliser `daily_participation_agrega`.

### 6. `main.go`

Ajouter le lancement du job d'agrégation pour chaque chaîne activée, après le démarrage du backfill :

```go
go gnovalidator.StartAggregator(db)
```

---

## Impact sur le sync au démarrage d'une nouvelle chaîne

1. `BackfillParallel` peuple `daily_participations` (données brutes, tous les blocs historiques).
2. `StartAggregator` (au prochain tick ou immédiatement au démarrage) détecte que `daily_participation_agrega`
   est vide pour cette chaîne et agrège tout l'historique disponible.
3. Après agrégation, les données brutes de plus de 7 jours sont purgées.

Aucun changement dans `sync.go` n'est nécessaire — la logique d'agrégation est indépendante du backfill.

---

## Gain de performance attendu

| Requête | Avant | Après |
|---|---|---|
| Uptime 30j (100 validateurs, 1 an de données) | ~4.3M lignes scannées | ~3 000 lignes (1 ligne/jour/validateur) |
| TX contrib mois en cours | ~5M lignes/mois | ~3 000 lignes |
| Missing blocks month | ~5M lignes/mois | ~3 000 lignes |
| Participation rate (rapport quotidien) | ~100K lignes/jour | 1 ligne/validateur |
| OperationTime (tout l'historique) | Croissant indéfiniment | Stable après agrégation |

---

## Ce qui NE change PAS

- La table `daily_participations` reste la source de vérité pour les données récentes.
- Toute la logique d'alerte (`WatchValidatorAlerts`, `SendResolveAlerts`, `daily_missing_series`) reste inchangée.
- Les métriques de chaîne (active validators, avg participation, chain height) restent sur `daily_participations`.
- Le modèle GORM `DailyParticipation` et les insertions en temps réel/backfill restent inchangés.

---

## Ordre d'implémentation suggéré

1. ✅ **Migration DB** — table `daily_participation_agrega` + indexes ajoutés dans `db_init.go`
2. ✅ **Aggregator** — `aggregator.go` implémenté avec upsert + purge
3. ✅ **Tests aggregator** — 5 tests passent (basic, today excluded, idempotent, multiple days, prune)
4. ✅ **Migrer db_metrics.go** — 6 fonctions migrées, tous les tests passent
5. ✅ **Migrer gnovalidator_report.go** — `CalculateRate` migrée
6. ✅ **Mettre à jour main.go** — `StartAggregator(db)` ajouté
7. **Benchmark** — comparer les temps de réponse des endpoints Prometheus avant/après

---

## Avancement

### ✅ Étape 1 — Migration DB (`db_init.go`)

- Struct GORM `DailyParticipationAgrega` ajoutée (PRIMARY KEY composite : `chain_id`, `addr`, `block_date`)
- Ajoutée à `AutoMigrate`
- Fonction `CreateAggregaIndexes` créée : indexes `idx_dpa_chain_date` et `idx_dpa_chain_addr_date`
- Appelée dans `InitDB` après `CreateOrReplaceIndexes`

> Note : GORM génère le nom de table `daily_participation_agregas` (pluriel automatique).

### ✅ Étape 2 — Aggregator (`gnovalidator/aggregator.go`)

- `StartAggregator(db)` : goroutine long-running, passe immédiate au démarrage puis toutes les heures
- `AggregateChain(chainID)` : UPSERT dans `daily_participation_agregas` pour tous les jours complets `< aujourd'hui`
- `PruneRawData(chainID)` : supprime les lignes `daily_participations` de plus de 7 jours
- Appelé dans `main.go` après `StartMetricsUpdater`

### ✅ Étape 3 — Tests aggregator (`gnovalidator/aggregator_test.go`)

- `TestAggregateChain_Basic` — vérifie les totaux pour 2 validateurs sur un jour passé
- `TestAggregateChain_TodayExcluded` — vérifie que le jour en cours n'est pas agrégé
- `TestAggregateChain_Idempotent` — deux passes successives donnent le même résultat
- `TestAggregateChain_MultipleDays` — chaque jour passé distinct produit sa propre ligne
- `TestPruneRawData` — les lignes anciennes sont supprimées, les récentes conservées

### ✅ Étape 4 — Migration `db_metrics.go`

6 fonctions migrées vers un UNION à 3 branches :
- **Branche 1** : `daily_participation_agregas` (jours complets passés — chemin rapide en production)
- **Branche 2** : `daily_participations` fallback LEFT JOIN (jours passés pas encore dans agrega — couvre les tests et les nouvelles chaînes)
- **Branche 3** : `daily_participations` aujourd'hui (le jour en cours n'est jamais dans agrega)

Fonctions migrées :
- `GetCurrentPeriodParticipationRate` — participation rate (mois/semaine/année/all_time)
- `UptimeMetricsaddr` — uptime 30 jours
- `TxContrib` — contribution TX (mois/semaine/année/all_time), total via CTE
- `MissingBlock` — blocs manquants (mois/semaine/année/all_time)
- `OperationTimeMetricsaddr` — dernier downtime/uptime (tout l'historique), CTEs combinées
- `GetFirstSeen` — première apparition (tout l'historique)

Fonctions conservées sur `daily_participations` (granularité bloc nécessaire) :
- `GetMissedBlocksWindow` (1h/24h/7d) — dans la fenêtre de rétention 7j
- `GetActiveValidatorCount`, `GetAvgParticipationRate`, `GetCurrentChainHeight`
- `GetTimeOfBlock`, alertes, monikers

> L'approche UNION garantit la compatibilité descendante : si agrega est vide (tests, nouvelle chaîne),
> la branche fallback prend le relais sans modifier les assertions de test.

### ✅ Étape 5 — Migration `gnovalidator_report.go`

- `CalculateRate(db, chainID, date)` : les 2 requêtes fusionnées en une seule avec UNION agrega + raw fallback
- Retourne par validateur : `total_blocks`, `participated_count`, `first_block_height`, `last_block_height`
- Le min/max global des hauteurs de bloc est calculé en Go lors du scan des résultats

### ✅ Étape 6 — Mise à jour `main.go`

- `gnovalidator.StartAggregator(db)` ajouté après `StartMetricsUpdater`
- Lancé au démarrage : passe d'agrégation immédiate puis toutes les heures
