# Fix — Validator First Active Block

## Problème

Lors d'un backfill (`BackfillParallel` / `BackfillRange`), le code itère sur `monikerMap`
qui contient **l'ensemble des validateurs actifs aujourd'hui**.

Pour chaque bloc, tout validateur absent des precommits reçoit une ligne
`participated = false` dans `daily_participations`.

Or un validateur accepté par la GovDAO au bloc 900 n'existait pas aux blocs 0–899.
En lui créant des lignes `participated = false` sur ces blocs, on lui attribue
des milliers de blocs manqués fictifs, ce qui fausse :

- l'uptime (`UptimeMetricsaddr`)
- les blocs manqués (`MissingBlock`, `CalculateConsecutiveMissedBlocks`)
- les alertes (seuils WARNING/CRITICAL déclenchés à tort)

---

## Cause racine

`BackfillParallel` (sync.go:211) et `BackfillRange` (sync.go:130) :

```go
// participated false
for addr, mon := range monikerMap {      // monikerMap = validateurs ACTUELS
    if _, ok := seen[addr]; ok { continue }
    rows = append(rows, dpRow{
        ...
        Participated: false,             // inséré même avant l'activation
    })
}
```

`monikerMap` ne contient aucune information sur le bloc d'activation de chaque
validateur. La fonction ne peut donc pas savoir si un validateur était déjà actif
au bloc `j.H`.

---

## Plan d'action

### Phase 1 — Modèle de données : ajouter `first_active_block`

Ajouter un champ `first_active_block` à la struct `AddrMoniker` dans
`db_init.go` :

```go
type AddrMoniker struct {
    Addr             string `gorm:"column:addr;primaryKey"`
    Moniker          string `gorm:"column:moniker;not null"`
    FirstActiveBlock int64  `gorm:"column:first_active_block;default:-1"`
}
```

`-1` = inconnu. GORM gère la migration via `AutoMigrate` (ajout de colonne).

---

### Phase 2 — Peupler `first_active_block` pour les validateurs existants

Pour les validateurs déjà présents dans `daily_participations`, le premier bloc
où `participated = 1` EST leur bloc d'activation réel :

```sql
UPDATE addr_monikers
SET first_active_block = (
    SELECT MIN(block_height)
    FROM daily_participations
    WHERE daily_participations.addr = addr_monikers.addr
      AND participated = 1
)
WHERE first_active_block = -1;
```

À exécuter une seule fois au démarrage, dans `InitDB` ou lors de
l'initialisation de la MonikerMap, après `AutoMigrate`.

Pour les validateurs **jamais vus** dans `daily_participations`,
`first_active_block` reste `-1` (inconnu — sera déterminé dynamiquement).

---

### Phase 3 — Détecter `first_active_block` dynamiquement

#### 3a. Pendant le backfill

Dans le worker de `BackfillParallel`, quand un validateur apparaît dans les
precommits d'un bloc (`participated = true`) **et que son `first_active_block`
est `-1`** dans la map transmise, enregistrer ce bloc comme son activation :

```go
// Lors du traitement des precommits (participated = true)
if firstActiveBlocks[addr] == -1 {
    firstActiveBlocks[addr] = j.H   // premier bloc observé
    // + UpsertAddrMoniker avec first_active_block = j.H
}
```

#### 3b. Pendant le temps réel (`CollectParticipation`)

Même logique : quand un validateur apparaît pour la première fois dans les
precommits du bloc courant et que son `first_active_block` est `-1`, mettre
à jour `addr_monikers`.

#### 3c. Option alternative : interroger valopers.Render

`valopers.Render(":addr")` expose potentiellement une date d'enregistrement.
Si la réponse contient le bloc de la GovDAO qui a accepté le validateur, on
peut l'utiliser directement dans `InitMonikerMap`.

À investiguer lors de l'implémentation — c'est plus précis mais dépend de la
structure du realm.

---

### Phase 4 — Modifier `BackfillParallel` et `BackfillRange`

Passer une map `firstActiveBlocks map[string]int64` aux deux fonctions.

Dans la boucle "participated false", sauter les blocs antérieurs à l'activation :

```go
for addr, mon := range monikerMap {
    if _, ok := seen[addr]; ok {
        continue // déjà ajouté comme participated=true
    }
    fab := firstActiveBlocks[addr]
    if fab > 0 && j.H < fab {
        continue // validateur pas encore actif à ce bloc
    }
    rows = append(rows, dpRow{
        ...
        Participated: false,
    })
}
```

`fab == -1` (inconnu) → on insère quand même (comportement actuel conservé,
le pire cas est un faux négatif sur les premiers blocs d'un validateur inconnu).

---

### Phase 5 — Modifier `CollectParticipation` (temps réel)

Même garde dans la boucle des `participated = false` :

```go
for valAddr, moniker := range MonikerMap {
    fab := getFirstActiveBlock(valAddr) // lit addr_monikers ou cache mémoire
    if fab > 0 && currentHeight < fab {
        continue
    }
    // insert participated=false
}
```

---

### Phase 6 — Nettoyage des données existantes (migration one-shot)

Après déploiement, supprimer les lignes parasites déjà en base :

```sql
DELETE FROM daily_participations dp
WHERE participated = 0
  AND EXISTS (
      SELECT 1 FROM addr_monikers am
      WHERE am.addr = dp.addr
        AND am.first_active_block > 0
        AND dp.block_height < am.first_active_block
  );
```

**Attention :** requête lourde sur grande table — à exécuter hors production
ou en mode maintenance (WAL mode préserve les lectures concurrentes).

---

## Résumé des fichiers à modifier

| Fichier | Changement |
| --- | --- |
| `db_init.go` | Ajouter `FirstActiveBlock int64` à `AddrMoniker` |
| `db_init.go` | Requête SQL one-shot de peuplement au démarrage |
| `db.go` | `UpsertAddrMoniker` accepte `firstActiveBlock int64` |
| `sync.go` | `BackfillParallel` + `BackfillRange` : param `firstActiveBlocks` + garde |
| `gnovalidator_realtime.go` | `CollectParticipation` : garde sur `first_active_block` |
| `valoper.go` | `InitMonikerMap` : charger `first_active_block` depuis DB en mémoire |

---

## Ordre d'implémentation recommandé

1. Phase 1 — struct + AutoMigrate (non breaking)
2. Phase 2 — peuplement au démarrage (requête SQL idempotente)
3. Phase 4 — backfill corrigé
4. Phase 5 — temps réel corrigé
5. Phase 3b — détection dynamique pendant les deux boucles
6. Phase 6 — nettoyage des données (après validation en staging)
