# Feat: Daily Report — RPC Enrichment

**Date:** 2026-04-20  
**Mise à jour:** 2026-04-20 (post-implémentation étapes 1–8 + retour terrain)  
**Branch:** `feat/daily-report-rpc-enrichment`

---

## Objectif

Enrichir le daily report et la commande `/status` avec des données RPC disponibles dans le repo gno
mais pas encore exploitées : voting power, changements de valset, peers réseau, mempool,
et flag d'intention de départ (`KeepRunning=false`).

---

## État d'avancement

| Étape | Description | Statut |
|---|---|---|
| 1 | `ValidatorSet` + `ValsetChanges` dans `ChainHealthSnapshot` | ✅ fait |
| 2 | Precommit bitmap par validateur (DumpConsensusState) | ✅ fait — **à revoir** (voir § Problèmes constatés) |
| 3 | `PeerCount` + `MempoolTxCount` dans le snapshot | ✅ fait |
| 4 | `KeepRunning` + `ServerType` depuis valopers realm | ✅ fait |
| 5 | Formatters `FormatHealthyReport`, `FormatStuckReport`, `/status` Telegram | ✅ fait — **à revoir** |
| 6 | 4 nouvelles métriques Prometheus | ✅ fait |
| 7 | Endpoint REST `GET /api/chain/:chainID/health` | ✅ fait |
| 8 | Security review + fix goroutine leak | ✅ fait |
| 9 | Refonte du format validateur (participation rate DB + monikers) | 🔜 à faire |

---

## Problèmes constatés après test terrain

Rendu actuel de `/status` sur test12 (chain saine, round 0) :

```
🟢 [test12] Chain status — block #397149 (0m ago)
Consensus: round 0 — Normal
Network: 5 peers | Mempool: 0 pending txs
Validator set (9 active):
  🔴 g15atj32de45nqgm68298aua8ayy4aujwyewegvd (g15atj32de...) precommit ✗ | power: 60
  🔴 g18kp360plxkmh3yal3juzffjuqa0ugys0963a80 (g18kp360pl...) precommit ✗ | power: 30
  ...
Infrastructure: 1 data-center
```

### Problème 1 — Precommit bitmap toujours à ✗ sur chain saine

**Cause :** `DumpConsensusState` retourne l'état des votes dans le **round en cours**. Quand la
chaîne vient de produire un bloc (round 0), le round suivant vient de démarrer — personne n'a
encore voté. La donnée est techniquement correcte mais afficher 🔴 partout sur une chain healthy
est trompeur et inutile.

**Décision :** Supprimer le precommit bitmap du rapport de chaîne saine. Le garder uniquement
pour le rapport `IsStuck` où il a du sens (la chaîne est bloquée, on veut savoir qui vote encore).

### Problème 2 — Moniker absent, adresse complète affichée

**Cause :** `ValidatorInfo.Moniker` n'est pas populé. L'étape 4 extrait `ServerType` depuis le
realm valopers mais ne remplit pas le moniker. Le formatter utilise `ValidatorInfo.Address` comme
fallback, ce qui affiche l'adresse complète en position "moniker" puis la version tronquée en
parenthèses.

**Décision :** Utiliser `GetMonikerMap(chainID)` (déjà en place dans le projet) pour résoudre
le moniker au moment du formatage.

### Problème 3 — Voting power en valeur brute peu lisible

Afficher `power: 60` sans contexte ne dit rien. Il faut le rapport au total du valset pour
comprendre le poids relatif de chaque validateur.

---

## Étape 9 — Refonte du format de la section validateurs

**Agent :** `add-telegram-command`  
**Fichiers :** `backend/internal/gnovalidator/health.go` (fonctions `Format*`), `backend/internal/telegram/validator.go`

### 9a — Remplacer le precommit bitmap par la participation rate DB

Dans `FormatHealthyReport` (chain saine) :

- **Supprimer** la section "precommit ✓/✗" basée sur `PrecommitBitmap`
- **Ajouter** la participation rate des derniers 100 blocs depuis la DB

`ChainHealthSnapshot` possède déjà `ValidatorRates map[string]ValidatorRate` (populé par
`CalculateRecentValidatorStatus`). Utiliser ce champ à la place.

Format cible (chain saine) :

```
Validator set (9 active — total power: 420):
  Top 5 worst performers (last 100 blocks):
  🔴 unknown     (g1c49...)  0.0% uptime |  7.1% power ⚠️ intends to leave
  🟡 aeddi-2     (g1qtx...) 87.3% uptime | 14.3% power
  🟢 slowval     (g1ghi...) 94.1% uptime | 14.3% power
  🟢 gnoops      (g1def...) 98.1% uptime | 14.3% power
  🟢 samourai    (g1abc...) 99.8% uptime | 14.3% power
  (4 others at 100%)
```

Règles :
- Trier par uptime **ascendant** (les pires en premier)
- Afficher les **5 pires** uniquement
- Si les validateurs restants sont tous à 100%, afficher une ligne de synthèse `(N others at 100%)`
- Si tous sont à 100%, afficher uniquement la ligne de synthèse `All 9 validators at 100% uptime`

Règles d'emoji uptime :
- 🟢 ≥ 95%
- 🟡 ≥ 80%
- 🔴 < 80%

Dans `FormatStuckReport` (chain bloquée) :

Garder le precommit bitmap ici — c'est le seul cas où il est informatif
(la chaîne ne progresse pas, on veut savoir qui vote encore dans le round courant).

### 9b — Résoudre les monikers via MonikerMap

Dans les fonctions `Format*`, appeler `GetMonikerMap(chainID)` pour résoudre le moniker
à partir de l'adresse. Fallback : adresse tronquée si le moniker est vide.

`FormatHealthyReport` et `FormatStuckReport` doivent recevoir `chainID string` en paramètre
(il est déjà passé, vérifier la signature actuelle).

### 9c — Voting power en pourcentage

Calculer `totalPower = sum(v.VotingPower for v in ValidatorSet)` dans le formatter.
Afficher `power%` = `v.VotingPower / totalPower * 100` avec une décimale.

### 9d — Adapter la version HTML Telegram

Même logique pour le handler `/status` dans `telegram/validator.go` —
utiliser la participation rate et les monikers résolus.

---

## Format cible complet après étape 9

### Chain saine

```
---
📊 [test12] Daily Summary — 2026-04-20

🟢 Block #397149 (0m ago) — Consensus round 0 — Normal
Network: 5 peers | Mempool: 0 pending txs

Validator set (9 active — total power: 420):
  Top 5 worst performers (last 100 blocks):
  🔴 unknown     (g1c49...)  0.0% uptime |  7.1% power
  🟡 aeddi-2     (g1qtx...) 87.3% uptime | 14.3% power
  🟢 slowval     (g1ghi...) 94.1% uptime | 14.3% power
  🟢 gnoops      (g1def...) 98.1% uptime | 14.3% power
  🟢 samourai    (g1abc...) 99.8% uptime | 14.3% power
  (4 others at 100%)

Infrastructure: 1 data-center

Valset changes (last 10):
  Block #45100 — g1xyz... added (power: 1000)
  Block #44900 — g1old... removed

Missed blocks last 24h:
  🔴 aeddi-2  (g1qtx...) 516 missed
  🔴 unknown  (g1c49...) 8937 missed
```

### Chain bloquée

```
---
📊 [test12] Daily Summary — 2026-04-20

🚨 Block #234888 (3d 4h ago) — Consensus round 713 — STUCK
Network: 3 peers | Mempool: 0 pending txs

Active votes this round (5/9 voting):
  ✓ samourai   (g1abc...)
  ✓ gnoops     (g1def...)
  ✗ unknown    (g1c49...) — offline
  ...

Last known participation (100 blocks before freeze):
  🟢 samourai  (g1abc...) 99.8% | 14.3% power
  ...
```

---

## Nouvelles sources de données (rappel)

| Source RPC / Realm | Méthode | Données exposées |
|---|---|---|
| `rpcClient.Validators(height)` | RPC | Voting power par validateur, taille du valset |
| `rpcClient.DumpConsensusState()` | RPC | Bitmask prevote/precommit — utile uniquement sur chain stuck |
| `rpcClient.NetInfo()` | RPC | Nombre de peers |
| `rpcClient.NumUnconfirmedTxs()` | RPC | Taille du mempool |
| `vm/qrender` → `gno.land/r/sys/validators/v2` | ABCI | 10 derniers changements de valset |
| `vm/qrender` → `gno.land/r/gnops/valopers:<addr>` | ABCI | `ServerType` (cloud/on-prem/data-center) |
| `CalculateRecentValidatorStatus` (DB) | SQLite | Participation rate derniers 100 blocs |
| `GetMonikerMap(chainID)` (in-memory) | Map | Monikers résolus |

---

## Ce qui n'est PAS dans ce plan

- **Alertes sur voting power** (baisse soudaine) — nécessite baseline historique, feat séparé
- **`BlockResults()` pour analyse gas/erreurs** — pertinent pour dashboard graphique, pas rapport texte
- **`ConsensusParams()` changements** — événement rare, alerte ponctuelle dédiée
- **Auth sur `/api/chain/:chainID/health`** — endpoint public intentionnel pour dashboard,
  décision assumée (6+ appels RPC par requête, acceptable pour usage interne)

---

## Fichiers modifiés / créés

| Fichier | Changement |
|---|---|
| `gnovalidator/health.go` | `ChainHealthSnapshot`, `ValidatorInfo`, RPC calls, formatters — ✅ |
| `gnovalidator/Prometheus.go` | 4 nouvelles métriques — ✅ |
| `api/api.go` | Endpoint `/api/chain/:chainID/health` — ✅ |
| `telegram/validator.go` | Bridge fields + HTML formatter — ✅ |
| `main.go` | Bridge closure enrichie — ✅ |
| `gnovalidator/health.go` | Étape 9 : participation rate + monikers dans formatters — ✅ |
| `telegram/validator.go` | Étape 9 : HTML variant mis à jour — ✅ |
| `gnovalidator/health.go` | Étape 10 : uptime calculé sur 24h — 🔜 |
| `gnovalidator/Prometheus.go` | Étape 10 : mise à jour du cycle de calcul — 🔜 |

Aucune migration DB. Aucune nouvelle dépendance externe.

---

## Étape 10 — Aligner la fenêtre uptime sur 24h

**Agent :** `senior-go-gnoland`  
**Motivation :** Le rapport affiche "96.1% uptime" calculé sur les 100 derniers blocs (~100s sur
une chain à 1 block/s) et "510 missed blocks" calculé sur 24h. Les deux métriques parlent de la
même chose mais sur des fenêtres incompatibles — un opérateur ne peut pas les lire ensemble sans
confusion.

### Problème actuel

`FetchChainHealthSnapshot` appelle `CalculateRecentValidatorStatus(db, chainID, 100)` — fenêtre
de 100 blocs en nombre. Sur test12 (1 block/s), 100 blocs = ~1 minute 40. Sur une chain plus lente
(1 block/6s), 100 blocs = ~10 minutes. La fenêtre est donc variable et non comparable à 24h.

### Changement requis

#### 10a — Nouvelle fonction `CalculateValidatorStatusLast24h`

**Fichier :** `backend/internal/gnovalidator/health.go`

Créer une nouvelle fonction basée sur une fenêtre temporelle fixe :

```go
func CalculateValidatorStatusLast24h(db *gorm.DB, chainID string) (map[string]ValidatorRate, int64, int64, error)
```

Requête SQL (même structure que `CalculateRecentValidatorStatus`, fenêtre changée) :

```sql
SELECT addr, MAX(moniker) AS moniker,
    COUNT(*) AS total_blocks,
    SUM(CASE WHEN participated THEN 1 ELSE 0 END) AS participated_count,
    MIN(block_height) AS first_block,
    MAX(block_height) AS last_block
FROM daily_participations
WHERE chain_id = ?
  AND date >= datetime('now', '-24 hours')
GROUP BY addr
```

#### 10b — Remplacer l'appel dans `FetchChainHealthSnapshot`

**Fichier :** `backend/internal/gnovalidator/health.go`

Remplacer :
```go
rates, minBlock, maxBlock, err := CalculateRecentValidatorStatus(db, chainID, GetThresholds().RecentBlocksWindow)
```

Par :
```go
rates, minBlock, maxBlock, err := CalculateValidatorStatusLast24h(db, chainID)
```

`snap.ValidatorRates` (utilisé par `/status` Telegram et le daily report) sera alors aligné sur 24h.

#### 10c — Garder `CalculateRecentValidatorStatus` pour Prometheus

`CalculateRecentValidatorStatus` est toujours utilisé dans `Prometheus.go` pour
`gnoland_validator_uptime` (fenêtre 500 blocs — comportement voulu distinct). Ne pas le supprimer
ni le modifier.

#### 10d — Mettre à jour le label dans les formatters

Dans `FormatHealthyReport` et `FormatStuckReport`, le header de section devient :

```
Validator set (9 active — total power: 420):
  Top 5 worst performers (last 24h):
```

Au lieu de l'actuel `(last 100 blocks)` implicite.

Dans `formatChainHealthMessage` (Telegram), même changement.

### Impact

| Composant | Avant | Après |
|---|---|---|
| Uptime dans `/status` et daily report | ~100 derniers blocs | 24 dernières heures |
| Uptime Prometheus `gnoland_validator_uptime` | 500 derniers blocs | inchangé |
| Section "Missed blocks last 24h" | 24h | inchangé |
| Cohérence des deux sections | ❌ fenêtres différentes | ✅ même fenêtre |

### Critères de succès

- Sur test12 : le validateur avec 8338 missed blocks sur 24h affiche un uptime proche de 0%
- Sur test12 : aeddi-2 avec 510 missed blocks affiche un uptime cohérent avec sa section missed
- `go test ./internal/gnovalidator/...` passe
- `go build ./...` propre
