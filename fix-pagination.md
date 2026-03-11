# Fix Pagination — Analyse et Correctifs

## Contexte

La pagination via inline buttons a été implémentée dans `backend/internal/telegram/validator.go` et `backend/internal/telegram/telegram.go`. Elle fonctionne correctement fonctionnellement, mais génère une charge CPU anormalement élevée à chaque navigation.

---

## Problème central

Chaque clic sur ⬅️ Prev ou ➡️ Next déclenche cette chaîne complète :

```text
callback → buildPaginatedResponse
         → formatUptime / formatParticipationRAte / FormatTxcontrib / ...
         → database.*Metrics(db)   ← FULL TABLE SCAN à chaque fois
         → sort.Slice(all, ...)    ← TRI INTÉGRAL en mémoire Go
         → filter*(all, filter)    ← FILTRE INTÉGRAL en mémoire Go
         → paginate(len, page, limit) → slice [start:end]
```

Les données de participation ne changent pas à la seconde. Pourtant, naviguer de la page 1 à la page 3 déclenche 3 requêtes SQL complètes + 3 tris + 3 filtres identiques.

---

## Problème 1 — `http.Client` recréé à chaque appel Telegram

**Fichier** : `backend/internal/telegram/telegram.go`

**Localisation** : lignes 88, 132, 174, 208 — dans chaque fonction HTTP (`SendMessageTelegram`, `SendMessageTelegramWithMarkup`, `EditMessageTelegramWithMarkup`, `AnswerCallbackQuery`)

**Code actuel** :

```go
client := &http.Client{Timeout: 10 * time.Second}
```

**Problème** : Chaque navigation génère 3 appels HTTP vers l'API Telegram (AnswerCallbackQuery + EditMessageText + éventuellement un autre). Chacun instancie un nouveau `http.Client`, ce qui empêche toute réutilisation du pool de connexions TCP (`keep-alive`). Le transport TCP est réétabli à chaque appel.

**Impact** : Élevé — c'est la source principale de CPU inutile.

**Fix** : Déclarer un client partagé au niveau du package :

```go
// à ajouter en haut de telegram.go, avec les autres vars
var telegramHTTPClient = &http.Client{Timeout: 10 * time.Second}
```

Puis remplacer les 4 instanciations locales par `telegramHTTPClient`.

---

## Problème 2 — `AnswerCallbackQuery` ne ferme pas `resp.Body`

**Fichier** : `backend/internal/telegram/telegram.go`

**Localisation** : fonction `AnswerCallbackQuery`, ligne ~210

**Code actuel** :

```go
resp, err := client.Do(req)
if err != nil {
    return fmt.Errorf("do request: %w", err)
}
return nil  // ← resp.Body jamais fermé
```

**Problème** : Sans `defer resp.Body.Close()`, la connexion TCP n'est jamais rendue au pool de transport. Avec de nombreux clics sur les boutons, les file descriptors s'accumulent jusqu'à saturation.

**Impact** : Élevé — leak de connexions progressif.

**Fix** :

```go
resp, err := client.Do(req)
if err != nil {
    return fmt.Errorf("do request: %w", err)
}
defer resp.Body.Close()
return nil
```

---

## Problème 3 — Pas de cache entre les pages

**Fichiers** : `backend/internal/telegram/validator.go`

**Localisation** : toutes les fonctions `format*` — `formatUptime`, `formatParticipationRAte`, `FormatTxcontrib`, `formatMissing`, `formatOperationTime`

**Problème** : Un utilisateur qui navigue pages 1 → 2 → 3 sur `/uptime` déclenche 3 appels complets à `database.UptimeMetricsaddr(db)`. Les données ne changent pas à cette échelle de temps.

**Impact** : Moyen — redondant, mais acceptable avec peu d'utilisateurs simultanés.

**Fix** : Cache in-memory avec TTL de 30 à 60 secondes, clé `(cmdKey, period)` :

```go
type cacheEntry struct {
    data      any
    expiresAt time.Time
}

var (
    metricsCache   = map[string]cacheEntry{}
    metricsCacheMu sync.Mutex
)

func getCached(key string) (any, bool) {
    metricsCacheMu.Lock()
    defer metricsCacheMu.Unlock()
    e, ok := metricsCache[key]
    if !ok || time.Now().After(e.expiresAt) {
        return nil, false
    }
    return e.data, true
}

func setCached(key string, data any, ttl time.Duration) {
    metricsCacheMu.Lock()
    defer metricsCacheMu.Unlock()
    metricsCache[key] = cacheEntry{data: data, expiresAt: time.Now().Add(ttl)}
}
```

Utilisation dans `formatUptime` :

```go
const cacheTTL = 45 * time.Second

func formatUptime(db *gorm.DB, page, limit int, filter, sortOrder string) (...) {
    key := "uptime"
    var results []database.UptimeMetrics
    if cached, ok := getCached(key); ok {
        results = cached.([]database.UptimeMetrics)
    } else {
        results, err = database.UptimeMetricsaddr(db)
        if err != nil { ... }
        setCached(key, results, cacheTTL)
    }
    // suite identique...
}
```

---

## Problème 4 — Tri et filtre : deux passes Go au lieu de SQL

**Fichiers** : `backend/internal/telegram/validator.go` + `backend/internal/database/db_metrics.go`

**Localisation** : toutes les fonctions `format*`

**Problème** : Le tri (`sort.Slice`) et le filtre (`filter*`) sont effectués en Go sur l'intégralité du résultat. L'ordre n'est pas cohérent entre les commandes (certaines filtrent avant de trier, d'autres après). Avec des données croissantes, ce sera coûteux.

**Impact** : Moyen — acceptable aujourd'hui avec ~50 validators, problématique à l'échelle.

**Fix idéal** : Pousser `ORDER BY`, `WHERE moniker LIKE ?`, `LIMIT ? OFFSET ?` directement dans les requêtes SQL de `db_metrics.go`. Exemple pour `UptimeMetricsaddr` :

```go
func UptimeMetricsaddr(db *gorm.DB, filter string, sortOrder string, limit, offset int) ([]UptimeMetrics, error) {
    var results []UptimeMetrics
    order := "DESC"
    if sortOrder == "asc" {
        order = "ASC"
    }
    q := db.Raw(`
        SELECT ... FROM ...
        WHERE (? = '' OR moniker LIKE ? OR addr LIKE ?)
        ORDER BY uptime ` + order + `
        LIMIT ? OFFSET ?
    `, filter, "%"+filter+"%", "%"+filter+"%", limit, offset)
    return results, q.Scan(&results).Error
}
```

Cela supprime les `sort.Slice` et `filter*` de `validator.go` et pousse le travail sur SQLite qui utilise ses index.

**Fix minimal (sans toucher aux signatures DB)** : Au moins combiner filtre et tri en une seule passe, et s'assurer que le filtre est appliqué avant le tri dans toutes les commandes (cohérence).

---

## Problème 5 — `searchState` non purgé proactivement

**Fichier** : `backend/internal/telegram/validator.go`

**Localisation** : variable `searchState` ligne ~800, fonction `HandleSearchInput`

**Problème** : Les états expirés ne sont supprimés que lorsque le même `chatID` envoie un nouveau message. La map grossit sans limite avec le temps.

**Impact** : Faible — fuite mémoire lente.

**Fix** : Lancer un ticker de nettoyage au démarrage du bot :

```go
func startSearchStateCleanup() {
    go func() {
        ticker := time.NewTicker(5 * time.Minute)
        defer ticker.Stop()
        for range ticker.C {
            searchStateMu.Lock()
            for chatID, s := range searchState {
                if time.Now().After(s.ExpiresAt) {
                    delete(searchState, chatID)
                }
            }
            searchStateMu.Unlock()
        }
    }()
}
```

À appeler depuis `BuildTelegramHandlers` ou `BuildTelegramCallbackHandler`.

---

## Récapitulatif par priorité

| # | Problème | Fichier | Impact | Difficulté |
| --- | -------- | ------- | ------ | ---------- |
| 1 | `http.Client` recréé à chaque appel | `telegram.go` | **Élevé** | Trivial (1 ligne) |
| 2 | `resp.Body` non fermé dans `AnswerCallbackQuery` | `telegram.go` | **Élevé** | Trivial (1 ligne) |
| 3 | Pas de cache entre les pages | `validator.go` | Moyen | ~30 lignes |
| 4 | Sort + filtre en Go au lieu de SQL | `validator.go` + `db_metrics.go` | Moyen | Refacto significatif |
| 5 | `searchState` non purgé | `validator.go` | Faible | ~15 lignes |

## Ordre d'application recommandé

1. **Appliquer 1 et 2 immédiatement** — corrections d'une ligne, risque zéro, gain immédiat.
2. **Appliquer 5** — quick win, prévient la fuite mémoire.
3. **Appliquer 3** — réduit les requêtes DB redondantes lors de la navigation.
4. **Appliquer 4** — refacto plus profonde, à faire sur une branche dédiée.
