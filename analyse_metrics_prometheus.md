# Analyse des métriques Prometheus — Gnomonitoring

> Généré le 2026-03-24. Basé sur l'analyse du code source sans aucune modification.

---

## Table des matières

1. [Architecture générale](#1-architecture-générale)
2. [Inventaire des métriques et leurs calculs](#2-inventaire-des-métriques-et-leurs-calculs)
3. [Incohérences critiques](#3-incohérences-critiques)
4. [Problèmes de sémantique](#4-problèmes-de-sémantique)
5. [Problèmes de performance (lenteur Telegram)](#5-problèmes-de-performance-lenteur-telegram)
6. [Question PostgreSQL](#6-question-postgresql)
7. [Tableau récapitulatif](#7-tableau-récapitulatif)

---

## 1. Architecture générale

```
main.go
  ├── gnovalidator.Init()              → enregistre les 13 métriques Prometheus
  ├── gnovalidator.StartMetricsUpdater(db) → goroutine, boucle toutes les 5 min
  │       └── UpdatePrometheusMetricsFromDB(db, chainID, ctx)  [timeout 2 min/chain]
  └── gnovalidator.StartPrometheusServer(port) → expose /metrics

Telegram bot → handlers dans telegram/validator.go
  └── appelle les mêmes fonctions database.* qu'utilise Prometheus
```

Les métriques sont mises à jour **toutes les 5 minutes**. Chaque chain est traitée en parallèle dans sa propre goroutine avec un timeout de 2 minutes.

---

## 2. Inventaire des métriques et leurs calculs

### 2.1 Phase 1 — Métriques par validateur

#### `gnoland_validator_participation_rate`
- **Fichier**: `gnovalidator/metric.go` — `CalculateValidatorRates()`
- **Fenêtre**: Les **10 000 derniers blocs** (correlated subquery sur MAX(block_height))
- **SQL**:
  ```sql
  SELECT dp.addr, COALESCE(am.moniker, dp.moniker) AS moniker,
         COUNT(*) AS total_blocks,
         SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) AS participated_blocks
  FROM daily_participations dp
  LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
  WHERE dp.chain_id = ?
    AND dp.block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 10000
  GROUP BY dp.addr
  ```
- **Calcul**: `participated_blocks / total_blocks * 100` (en Go, pas en SQL)
- **Appelé par**: Prometheus uniquement

---

#### `gnoland_validator_uptime`
- **Fichier**: `database/db_metrics.go` — `UptimeMetricsaddr()`
- **Fenêtre**: Les **500 derniers blocs** (deux étapes : MAX puis requête)
- **SQL**:
  ```sql
  -- Étape 1
  SELECT COALESCE(MAX(block_height), 0) FROM daily_participations WHERE chain_id = ?
  -- Étape 2
  SELECT COALESCE(am.moniker, dp.addr) AS moniker, dp.addr,
         100.0 * SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) / COUNT(*) AS uptime
  FROM daily_participations dp
  LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
  WHERE dp.chain_id = ? AND dp.block_height > ?   -- ? = maxHeight - 500
  GROUP BY dp.addr
  ```
- **Appelé par**: Prometheus ET Telegram (`/uptime`)

---

#### `gnoland_validator_tx_contribution`
- **Fichier**: `database/db_metrics.go` — `TxContrib()`
- **Fenêtre**: **Mois calendaire courant** (date >= début du mois, date < début du mois suivant)
- **SQL**:
  ```sql
  SELECT COALESCE(am.moniker, dp.addr) AS moniker, dp.addr,
    ROUND((SUM(dp.tx_contribution) * 100.0 /
      (SELECT SUM(tx_contribution) FROM daily_participations
       WHERE chain_id = ? AND date >= ? AND date < ?)), 1) AS tx_contrib
  FROM daily_participations dp
  LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
  WHERE dp.chain_id = ? AND dp.date >= ? AND dp.date < ?
  GROUP BY dp.addr
  ```
- **IMPORTANT**: `tx_contribution` est un **booléen** (0/1) dans la DB. `SUM()` compte le nombre de blocs où ce validateur était le proposer du bloc ET le bloc avait des transactions.
- **Appelé par**: Prometheus (hardcodé `"current_month"`) ET Telegram `/tx_contrib` (paramétrable)

---

#### `gnoland_validator_operation_time`
- **Fichier**: `database/db_metrics.go` — `OperationTimeMetricsaddr()`
- **Fenêtre**: Tout l'historique (pas de limite)
- **SQL**:
  ```sql
  WITH last_down AS (
    SELECT addr, chain_id, MAX(date) AS last_down_date
    FROM daily_participations WHERE chain_id = ? AND participated = 0
    GROUP BY chain_id, addr
  ),
  last_up AS (
    SELECT addr, chain_id, MAX(date) AS last_up_date
    FROM daily_participations WHERE chain_id = ? AND participated = 1
    GROUP BY chain_id, addr
  )
  SELECT COALESCE(am.moniker, ld.addr) AS moniker, ld.addr,
         ROUND(julianday(lu.last_up_date) - julianday(ld.last_down_date), 1) AS days_diff
  FROM last_down ld
  LEFT JOIN last_up lu ON lu.chain_id = ld.chain_id AND lu.addr = ld.addr
  LEFT JOIN addr_monikers am ON ...
  ```
- **Calcul**: `MAX(date_participation) - MAX(date_non_participation)`
- **Appelé par**: Prometheus ET Telegram (`/operation_time`)

---

#### `gnoland_validator_missing_blocks_month`
- **Fichier**: `database/db_metrics.go` — `MissingBlock()`
- **Fenêtre**: **Mois calendaire courant**
- **SQL**: `SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END)` sur le mois
- **Appelé par**: Prometheus ET Telegram (`/missing`)

---

#### `gnoland_validator_first_seen_unix`
- **Fichier**: `database/db_metrics.go` — `GetFirstSeen()`
- **Fenêtre**: Tout l'historique
- **SQL**: `MIN(dp.date)` où `participated = 1`
- **Conversion**: Parsée en Go avec deux layouts possibles, convertie en Unix timestamp
- **Appelé par**: Prometheus uniquement

---

#### `gnoland_missed_blocks` et `gnoland_consecutive_missed_blocks`
- **Fichier**: `gnovalidator/metric.go`
- **`missed_blocks`**: blocs manqués **aujourd'hui seulement** (`date >= date('now')`)
- **`consecutive_missed_blocks`**: streak calculé en Go sur les **200 derniers blocs**
- **Appelé par**: Prometheus uniquement

---

### 2.2 Phase 2 — Métriques par chain

#### `gnoland_chain_active_validators`
- **Fenêtre**: 100 derniers blocs — `COUNT(DISTINCT addr) WHERE participated = 1`

#### `gnoland_chain_avg_participation_rate`
- **Fenêtre**: 100 derniers blocs — `AVG(CAST(participated AS FLOAT)) * 100`

#### `gnoland_chain_current_height`
- `MAX(block_height)` — pas de fenêtre

Toutes les trois utilisent un `MAX(block_height)` préalable. **Trois requêtes MAX séparées** pour la même chain lors d'un même cycle de mise à jour.

---

### 2.3 Phase 3 — Métriques d'alertes

#### `gnoland_active_alerts`
```sql
WITH latest_alerts AS (
    SELECT addr, MAX(sent_at) as last_sent
    FROM alert_logs WHERE chain_id = ?
    GROUP BY addr
)
SELECT COUNT(*) FROM alert_logs al
INNER JOIN latest_alerts la ON al.addr = la.addr AND al.sent_at = la.last_sent
WHERE al.chain_id = ? AND al.level = ?
```
**Sémantique réelle**: compte les validateurs dont la **dernière alerte** a le niveau donné. Ce n'est pas "non résolu" au sens strict.

#### `gnoland_alerts_total`
```sql
SELECT COUNT(*) FROM alert_logs WHERE chain_id = ? AND level = ?
```
Comptage cumulatif de toutes les alertes envoyées.

---

## 3. Incohérences critiques

### 🔴 IC-1 : `participation_rate` Prometheus ≠ `/status` Telegram

C'est la cause principale des différences de valeurs entre Prometheus et Telegram.

| | Prometheus (`gnoland_validator_participation_rate`) | Telegram (`/status`) |
|---|---|---|
| **Fonction** | `CalculateValidatorRates()` | `GetCurrentPeriodParticipationRate()` |
| **Fichier** | `gnovalidator/metric.go` | `database/db_metrics.go` |
| **Fenêtre** | 10 000 derniers **blocs** | Mois calendaire courant (**date**) |
| **Type** | Glissant, basé sur blocs | Calendaire, reset le 1er du mois |

**Exemple concret**: au 25 du mois, si un validateur a bien participé les 10 derniers jours mais a raté beaucoup de blocs début de mois :
- Prometheus (10K blocs ≈ 30 jours) : montre les 30 derniers jours
- Telegram (mois en cours) : montre depuis le 1er du mois

Les deux métriques peuvent diverger significativement selon la période et le comportement du validateur.

---

### 🔴 IC-2 : `tx_contribution` — booléen mal interprété + division par zéro silencieuse

**La colonne `tx_contribution` est un `bool` (0/1)**, pas un entier ni un float.

Elle vaut `true` uniquement si : le validateur était le **proposer** du bloc ET le bloc contenait des transactions.
```go
// sync.go ligne 210
TxContribution: hasTx && (addr == txProp)
```

**Le calcul SQL**:
```sql
SUM(dp.tx_contribution) * 100.0 / (SELECT SUM(tx_contribution) ...)
```

**Problème** : si aucun bloc du mois ne possède `tx_contribution = 1` (chaîne sans transactions, ou proposer non identifiable), le dénominateur vaut **0** → SQLite retourne **NULL** → la métrique affiche **0%** pour tous les validateurs sans erreur.

C'est la raison pour laquelle `tx_contribution` ne fonctionne pas sur certaines chaînes en production. Les causes possibles :
- La chaîne utilise un indexeur GraphQL qui ne fournit pas le champ proposer
- Le champ proposer n'est pas dans les données de bloc récupérées par le RPC
- La chaîne a été backfillée avec un code qui ne peuplait pas `tx_contribution`

---

### 🟠 IC-3 : `uptime` — Prometheus et Telegram utilisent la même fonction

Contrairement à la participation rate, `uptime` utilise bien **la même fonction** (`UptimeMetricsaddr`) des deux côtés. Les valeurs **devraient être identiques**, avec un décalage de 45 secondes max (TTL cache Telegram) + 5 minutes (cycle Prometheus).

Si tu observes des différences, cela peut venir de :
1. Le cache Telegram (45s) qui retourne des données stales pendant que Prometheus est plus récent
2. Un validateur qui a changé de moniker entre les deux appels (COALESCE sur `addr_monikers`)

---

### 🟠 IC-4 : `AlertLog` n'a pas de colonne `resolved_at`

Le schéma `AlertLog` (db_init.go ligne 101-112) **ne contient pas de colonne `resolved_at`**. La documentation CLAUDE.md mentionne `WHERE sent_at IS NOT NULL AND resolved_at IS NULL` mais ce n'est pas ce que fait le code réel.

La requête `GetActiveAlertCount` utilise une heuristique : "la dernière alerte de ce validateur a-t-elle le niveau X ?" Ce n'est pas la même chose qu'une alerte "non résolue".

Cas problématique : un validateur CRITICAL qui reçoit ensuite une alerte WARNING → il n'apparaît plus dans `active_alerts{level="CRITICAL"}` mais dans `level="WARNING"`, même si le problème CRITICAL n'est pas résolu.

---

## 4. Problèmes de sémantique

### 🟡 SEM-1 : `operation_time` — calcul incorrect pour la sémantique annoncée

**Description dans le code** : "Days since last validator downtime event"

**Calcul réel** :
```
julianday(MAX(date WHERE participated=1)) - julianday(MAX(date WHERE participated=0))
```

Ce calcul donne `last_participation_date - last_absence_date`. Ce n'est **pas** le temps depuis la dernière panne, mais le delta entre la dernière présence et la dernière absence.

**Cas problématiques** :
- Valeur **négative** si le validateur est actuellement down (last_down > last_up)
- Valeur très petite (ex: 0.3) si le validateur a eu une micro-absence hier mais participait il y a peu
- La valeur ne représente pas "depuis combien de temps le validateur est stable"

**Ce que la métrique devrait être** : `julianday('now') - julianday(last_down_date)` si `last_up > last_down`, sinon 0 ou négatif.

---

### 🟡 SEM-2 : `uptime` — fenêtre de 500 blocs très courte

500 blocs est hardcodé. Sur différentes chaînes :
- Temps de bloc 2s → 500 blocs = **~17 minutes**
- Temps de bloc 6s → 500 blocs = **~50 minutes**

Cette fenêtre est extrêmement sensible aux incidents ponctuels. Un redémarrage de 30 minutes peut faire chuter l'uptime à 0%, même si le validateur est à 99.9% sur 30 jours.

---

### 🟡 SEM-3 : `first_seen` — parsing de date fragile

Le parsing dans `Prometheus.go` (lignes 298-314) supporte deux formats :
```go
"2006-01-02 15:04:05-07:00"  // avec timezone
"2006-01-02 15:04:05"        // sans timezone
```

Si SQLite retourne une date au format `"2006-01-02"` (date seule, sans heure), les **deux parsers échouent silencieusement** et le validateur est exclu de la métrique `first_seen_unix` sans log d'erreur clair.

---

## 5. Problèmes de performance (lenteur Telegram)

### Causes identifiées

#### PERF-1 : SQLite — concurrence entre 3 acteurs

Trois composants accèdent à SQLite en même temps :
1. **Validator monitoring loop** : écriture continue à chaque bloc (~2-6s par bloc)
2. **Prometheus updater** : lecture toutes les 5 min (queries lourdes)
3. **Telegram bot** : lecture à la demande

SQLite en mode WAL permet un lecteur concurrent pendant une écriture, mais les **writes bloquent les autres writes** et les **heavy reads en WAL peuvent bloquer des checkpoints**. Le bot Telegram peut attendre que le validator monitoring loop libère le verrou.

#### PERF-2 : Cache Telegram trop court (45 secondes)

```go
const cacheTTL = 45 * time.Second
```

Sur une chaîne avec un bloc toutes les 2 secondes, ce cache expire très vite. Pour des commandes comme `/uptime` qui font des calculs lourds, 45s force une nouvelle requête SQL toutes les 45s par chat actif.

#### PERF-3 : `CalculateValidatorRates` — sous-requête corrélée

```sql
WHERE dp.block_height > (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - 10000
```

SQLite évalue cette sous-requête **une seule fois** (pas corrélée par ligne), donc ce n'est pas un N+1. Mais elle s'exécute sur une table potentiellement à 14M+ lignes (test11). L'index `idx_dp_chain_addr_blockheight` devrait couvrir cette requête mais la performance dépend de la sélectivité.

#### PERF-4 : 3 requêtes `MAX(block_height)` redondantes par cycle Prometheus

Dans `UpdatePrometheusMetricsFromDB`, les 3 métriques Phase 2 font chacune leur propre `MAX(block_height)` :
- `GetActiveValidatorCount` : 1 MAX
- `GetAvgParticipationRate` : 1 MAX
- `GetCurrentChainHeight` : 1 MAX (implicitement via `GetCurrentChainHeight`)

Soit **3 requêtes identiques** sur la même table lors du même cycle pour la même chain.

#### PERF-5 : Sous-requête scalaire dans `TxContrib`

```sql
SUM(dp.tx_contribution) * 100.0 / (SELECT SUM(tx_contribution) FROM daily_participations WHERE ...)
```

La sous-requête interne n'est pas corrélée (elle ne référence pas l'outer query), SQLite peut l'optimiser. Mais elle fait un full scan du mois courant **pour chaque appel** à `TxContrib()`, en plus du scan principal.

---

## 6. Question PostgreSQL

### Est-ce que migrer vers PostgreSQL aiderait ?

**Pour les métriques Prometheus (5 min cycle)** : probablement pas nécessaire. Les requêtes sont bien indexées et le timeout de 2 min/chain est largement suffisant. Le vrai gain serait minimal à moyen terme.

**Pour la lenteur du Telegram** : oui, PostgreSQL aiderait significativement car :
- Concurrence MVCC réelle (multi-readers/writers sans verrou exclusif)
- Connection pooling natif (pgx/pgbouncer)
- Planner de requêtes plus sophistiqué
- Les sous-requêtes scalaires sont mieux optimisées

**Cependant**, avant de migrer, il y a des gains rapides à faire sur SQLite :
1. **Augmenter le TTL du cache Telegram** de 45s à 2-3 minutes
2. **Activer `PRAGMA busy_timeout`** pour éviter les `database is locked` silencieux
3. **Partager la valeur MAX(block_height)** entre les 3 requêtes Phase 2
4. **Corriger le bug `tx_contribution`** (colonne de type entier plutôt que booléen, ou requête adaptée)

---

## 7. Tableau récapitulatif

| Métrique | Fenêtre Prometheus | Fenêtre Telegram | Cohérent ? | Problème |
|---|---|---|---|---|
| `participation_rate` | 10 000 blocs | Mois calendaire | ❌ **NON** | Fenêtres différentes |
| `uptime` | 500 blocs | 500 blocs | ✅ Oui | Fenêtre très courte |
| `tx_contribution` | Mois courant | Mois courant | ✅ Oui | Booléen → div/0 sur certaines chains |
| `operation_time` | Tout historique | Tout historique | ✅ Oui | Formule sémantiquement incorrecte |
| `missing_blocks_month` | Mois courant | Mois courant | ✅ Oui | — |
| `first_seen_unix` | Tout historique | N/A | — | Parsing date fragile |
| `missed_blocks` | Aujourd'hui | N/A | — | Réinitialisé chaque jour à minuit |
| `consecutive_missed_blocks` | 200 blocs | N/A | — | — |
| `active_alerts` | — | — | — | Pas de `resolved_at` → heuristique imprécise |

---

## Résumé des actions prioritaires

| Priorité | Problème | Impact |
|---|---|---|
| 🔴 P0 | `tx_contribution` retourne 0 sur chains sans proposer data | Métrique muette sans erreur |
| 🔴 P0 | `participation_rate` Prometheus ≠ Telegram `/status` | Confusion utilisateur |
| 🟠 P1 | `uptime` — fenêtre 500 blocs trop courte et incohérente | Valeurs instables |
| 🟠 P1 | `consecutive_missed_blocks` — snapshot sans historique temporel | Pas de courbe possible |
| 🟠 P1 | `operation_time` — formule incorrecte | Valeurs négatives ou trompeuses |
| 🟠 P1 | Lenteur Telegram — TTL cache trop court | UX dégradée |
| 🟡 P2 | 3× `MAX(block_height)` redondants par cycle | Requêtes inutiles |
| 🟡 P2 | `first_seen_unix` — parsing date peut échouer silencieusement | Validateurs manquants |
| 🟡 P2 | `active_alerts` — pas de colonne `resolved_at` | Sémantique imprécise |

---

## 8. Plan de modifications (validé)

Les 4 modifications demandées sont documentées ci-dessous avec les fichiers touchés, la logique de changement et les points d'attention.

---

### MOD-1 : `participation_rate` → aligner sur le mois calendaire

**Objectif** : Prometheus et Telegram `/status` affichent la même valeur.

**Changement** : Dans `Prometheus.go`, remplacer l'appel à `CalculateValidatorRates()` par `database.GetCurrentPeriodParticipationRate(db, chainID, "current_month")`.

**Fichiers touchés** :

- `backend/internal/gnovalidator/Prometheus.go` — ligne 206, remplacer l'appel
- `backend/internal/gnovalidator/metric.go` — `CalculateValidatorRates()` devient inutilisée, à supprimer (ou garder pour usage futur)

**Détail du changement** :

```go
// AVANT (Prometheus.go ~ligne 206)
stats, err := CalculateValidatorRates(db, chainID)
// ...
for _, stat := range stats {
    ValidatorParticipation.WithLabelValues(chainID, stat.Address, stat.Moniker).Set(stat.Rate)
}

// APRÈS
rates, err := database.GetCurrentPeriodParticipationRate(db, chainID, "current_month")
// ...
for _, r := range rates {
    ValidatorParticipation.WithLabelValues(chainID, r.Addr, r.Moniker).Set(r.ParticipationRate)
}
```

**Points d'attention** :
- Le type de retour change : `[]ValidatorStat` → `[]database.ParticipationRate` (champs `Addr`, `Moniker`, `ParticipationRate`)
- La métrique Prometheus garde le même nom (`gnoland_validator_participation_rate`), aucun impact sur les dashboards existants
- Le test `Prometheus_test.go` qui teste `ValidatorParticipation` devra être mis à jour (les données de test utilisent `block_height` pour la fenêtre ; il faudra ajouter un champ `date` dans les fixtures)
- Supprimer `CalculateValidatorRates()` de `metric.go` si elle n'est plus appelée nulle part

---

### MOD-2 : `uptime` → 30 derniers jours (Prometheus + Telegram)

**Objectif** : Fenêtre significative (~30 jours), cohérente entre Prometheus et Telegram, et maintenable sur le long terme.

**Changement** : Modifier `UptimeMetricsaddr()` dans `db_metrics.go` pour utiliser une fenêtre date-based de 30 jours au lieu de 500 blocs.

**Fichier touché** :

- `backend/internal/database/db_metrics.go` — fonction `UptimeMetricsaddr()` lignes 159-190

**Détail du changement** :

```go
// AVANT : deux étapes (MAX block_height, puis filtre sur height > max - 500)

// APRÈS : filtrage date-based
query := `
    SELECT
        COALESCE(am.moniker, dp.addr) AS moniker,
        dp.addr,
        100.0 * SUM(CASE WHEN dp.participated THEN 1 ELSE 0 END) / COUNT(*) AS uptime
    FROM daily_participations dp
    LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
    WHERE
        dp.chain_id = ?
        AND dp.date >= date('now', '-30 days')
    GROUP BY dp.addr
    ORDER BY uptime ASC`
```

**Avantages** :
- Même fenêtre quelle que soit la vitesse de la chain (pas de biais lié au temps de bloc)
- L'index `idx_dp_chain_date_addr` couvre cette requête (chain_id + date)
- La requête devient une seule étape (suppression du MAX préalable)

**Points d'attention** :
- Sur les chains avec beaucoup de validateurs et de blocs/jour (ex: test11 à 2s/bloc = ~1.3M lignes sur 30 jours), le filtre `date >= date('now', '-30 days')` peut encore être lent si l'index sur `date` n'est pas efficace. **Vérifier avec `EXPLAIN QUERY PLAN`** sur prod avant de déployer.
- Le premier mois de données d'une nouvelle chain donnera un uptime basé sur moins de 30 jours — c'est acceptable.
- Les tests unitaires de `UptimeMetricsaddr` utilisent des `block_height` pour simuler la fenêtre ; il faudra adapter les fixtures pour utiliser des dates relatives à `now` (ex: `time.Now().AddDate(0, 0, -5)` pour simuler "5 jours dans la fenêtre").

---

### MOD-3 : `tx_contribution` → corriger la division par zéro silencieuse

**Problème précis** : La colonne `tx_contribution` est un `bool` (0/1). Si aucun bloc du mois n'a de proposer identifié (= `SUM(tx_contribution) = 0` pour toute la chain), le dénominateur vaut 0 → SQLite retourne NULL → la métrique vaut 0 pour tous les validateurs sans erreur ni log.

**Changement** : Utiliser `NULLIF()` pour transformer une division par zéro en NULL explicite, puis retourner NULL (0.0 en Go) avec un log d'avertissement distinct.

**Fichier touché** :

- `backend/internal/database/db_metrics.go` — fonction `TxContrib()` lignes 192-217

**Détail du changement SQL** :

```sql
-- AVANT
ROUND((SUM(dp.tx_contribution) * 100.0 /
    (SELECT SUM(tx_contribution) FROM daily_participations
     WHERE chain_id = ? AND date >= ? AND date < ?)), 1) AS tx_contrib

-- APRÈS : NULLIF protège contre la division par zéro
ROUND((SUM(dp.tx_contribution) * 100.0 /
    NULLIF((SELECT SUM(tx_contribution) FROM daily_participations
            WHERE chain_id = ? AND date >= ? AND date < ?), 0)), 1) AS tx_contrib
```

**Avec ce fix** :
- Si le total est 0, chaque validateur reçoit `NULL` au lieu de `0.0`
- En Go, le champ `TxContrib float64` recevra `0.0` (valeur zéro de Go) → comportement identique en surface, mais...
- Dans `Prometheus.go`, on peut détecter que `len(txStats) > 0` mais toutes les valeurs sont 0 et logguer un warning explicite : `"⚠️ [chain] TxContribution: total=0, proposer data absent"`

**Note** : Le vrai fix à terme est de vérifier **pourquoi** `tx_contribution = false` pour toutes les lignes sur ces chains. Causes possibles à investiguer par chain :
1. La chain a été backfillée avec `sync.go` (`BackfillRange`) qui ne renseigne peut-être pas le proposer correctement
2. Le RPC de la chain ne fournit pas l'information sur le proposer de bloc
3. Le format de l'adresse proposer ne correspond pas aux adresses validateurs dans la DB

---

### MOD-4 : `consecutive_missed_blocks` → courbe temporelle

**Objectif** : Pouvoir tracer dans Grafana/Prometheus une courbe `(temps, nb_blocs_manqués)` sur une période, plutôt qu'un simple snapshot du streak courant.

**Compréhension du besoin** : Prometheus scrape les métriques toutes les 15-30 secondes (configurable). Si on expose une gauge "blocs manqués sur les dernières X heures/jours", Prometheus construit automatiquement la time-series. La courbe se lit dans Grafana avec un simple graph panel.

**Architecture proposée** : Remplacer l'unique métrique `gnoland_consecutive_missed_blocks` (streak instantané) par une nouvelle métrique fenêtrée par période.

**Nouvelle métrique** : `gnoland_missed_blocks_window`
- **Type** : Gauge
- **Labels** : `chain`, `validator_address`, `moniker`, `window` (`"1h"`, `"24h"`, `"7d"`)
- **Valeur** : nombre de blocs manqués dans la fenêtre temporelle

Cela donne 3 time-series par validateur : sur 1h, 24h, 7 jours. Grafana peut alors afficher l'évolution de chaque courbe dans le temps.

**Fichiers touchés** :

- `backend/internal/gnovalidator/Prometheus.go` — ajout de la nouvelle métrique, appel dans `UpdatePrometheusMetricsFromDB`
- `backend/internal/database/db_metrics.go` — nouvelle fonction `GetMissedBlocksWindow(db, chainID, window string)`
- `backend/internal/gnovalidator/Prometheus.go` — suppression (optionnelle) de l'ancienne `ConsecutiveMissedBlocks`

**Détail SQL pour la nouvelle fonction** :

```sql
-- Blocs manqués sur les X dernières heures/jours (paramétré par une date calculée en Go)
SELECT
    COALESCE(am.moniker, dp.addr) AS moniker,
    dp.addr,
    SUM(CASE WHEN dp.participated = 0 THEN 1 ELSE 0 END) AS missed_count
FROM daily_participations dp
LEFT JOIN addr_monikers am ON am.chain_id = dp.chain_id AND am.addr = dp.addr
WHERE dp.chain_id = ?
  AND dp.date >= ?    -- borne calculée en Go : time.Now().Add(-windowDuration)
GROUP BY dp.addr
```

**Appel dans Prometheus.go** :

```go
windows := map[string]time.Duration{
    "1h":  time.Hour,
    "24h": 24 * time.Hour,
    "7d":  7 * 24 * time.Hour,
}
for label, dur := range windows {
    since := time.Now().Add(-dur)
    stats, err := database.GetMissedBlocksWindow(db, chainID, since)
    // ...
    for _, s := range stats {
        MissedBlocksWindow.WithLabelValues(chainID, s.Addr, s.Moniker, label).Set(float64(s.MissedCount))
    }
}
```

**Dans Grafana** : une query `gnoland_missed_blocks_window{window="24h"}` donne la courbe des blocs manqués sur 24h au fil du temps, pour chaque validateur.

**Points d'attention** :
- L'ancienne métrique `gnoland_consecutive_missed_blocks` peut être gardée en parallèle temporairement pour ne pas casser les alertes existantes
- Les 3 fenêtres (1h, 24h, 7d) triplent le nombre de time-series → à surveiller si beaucoup de validateurs (cardinality)
- L'index `idx_dp_chain_date_addr` est utilisé par cette requête (filtre sur `chain_id + date`)

---

### Tableau des modifications

| # | Métrique | Fichier principal | Complexité | Tests à mettre à jour |
|---|---|---|---|---|
| MOD-1 | `participation_rate` | `Prometheus.go`, `metric.go` | Faible | `Prometheus_test.go` |
| MOD-2 | `uptime` | `db_metrics.go` | Faible | `db_metrics_test.go`, `Prometheus_test.go` |
| MOD-3 | `tx_contribution` | `db_metrics.go` | Faible | `db_metrics_test.go` |
| MOD-4 | `consecutive_missed_blocks` → `missed_blocks_window` | `Prometheus.go`, `db_metrics.go` | Moyenne | `Prometheus_test.go` |
