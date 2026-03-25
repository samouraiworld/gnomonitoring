# Log Cleanup Plan

## Convention à appliquer

**Préfixe uniforme** : `[component][chain]` sur tous les logs.

Exemples : `[monitor][betanet]`, `[aggregator][betanet]`, `[metrics][betanet]`, `[govdao][betanet]`, `[telegram]`, `[db]`, `[report]`, `[valoper][betanet]`

**Emojis** : supprimés de tous les logs internes. Conservés uniquement sur les messages envoyés en notification (Discord/Slack/Telegram).

**Niveaux de logs** :
- **Lifecycle** : une ligne au démarrage et une ligne de résumé par cycle. Toujours affiché.
- **Warning** : opération dégradée mais en cours (retry, fallback). Toujours affiché.
- **Error** : opération échouée, action requise. Toujours affiché.
- **Debug / per-item** : par bloc, par validateur, par ligne de DB. **À supprimer entièrement.**

---

## Priorité 1 — Suppressions immédiates (logs très bruyants)

Ces logs génèrent des centaines ou milliers de lignes par heure en fonctionnement normal.

| Fichier | Log actuel | Action | Raison |
|---------|-----------|--------|--------|
| `gnovalidator/gnovalidator_realtime.go` | `✅ Saved participation for %s (%s) at height %d: %v` | Supprimer | Se déclenche pour chaque validateur à chaque bloc → ~600 lignes/min avec 10 validateurs |
| `gnovalidator/gnovalidator_realtime.go` | `⏱️ Skipping resolve alert for %s : already sent` | Supprimer | Se déclenche pour chaque validateur toutes les 20 secondes en fonctionnement normal |
| `gnovalidator/gnovalidator_realtime.go` | `==========================Start resolv Alert==========00==` | Supprimer | Bannière décorative avec typo, toutes les 20 secondes par chain |
| `database/db_metrics.go` | `📦 Loaded from DB — Addr: %s, Moniker: %s` | Supprimer | Se déclenche par validateur à chaque refresh moniker (toutes les 5 min) |
| `database/db_metrics.go` | `==========Start Get Participate Rate` | Supprimer | Toutes les 5 min par chain + chaque commande Telegram `/status` |
| `database/db_metrics.go` | `start %s` et `end %s` dans `GetAlertLog` | Supprimer | Debug de paramètres de requête, se déclenche sur chaque appel API `/alerts` |
| `gnovalidator/Prometheus.go` | 10x `→ Calculating XYZ...` et `→ XYZ: N validators` | Supprimer | Rapport d'avancement étape par étape, toutes les 5 min par chain |
| `gnovalidator/Prometheus.go` | `📈 [%s] PHASE 2: Chain metrics` et `🚨 [%s] PHASE 3: Alert metrics` | Supprimer | Labels de phase, aucune valeur opérationnelle |
| `gnovalidator/Prometheus.go` | `-> Processing chain: %s` | Supprimer | Redondant avec les lignes de début/fin déjà présentes |
| `gnovalidator/gnovalidator_report.go` | `log.Println(msg)` (corps entier du rapport) | Supprimer | Imprime le rapport complet en console à chaque envoi ; déjà livré via Telegram/webhook |
| `gnovalidator/gnovalidator_report.go` | `fmt.Printf("last block: %d\n", height)` | Supprimer | `fmt.Printf` sans timestamp, log de debug oublié en production |
| `gnovalidator/gnovalidator_report.go` | `[CalculateRate] date=%s chain=%s` | Supprimer | Trace d'entrée de fonction, appelée à chaque envoi de rapport |
| `gnovalidator/valoper.go` | `🔹 Validator: %s — Moniker: %s` dans `InitMonikerMap` | Supprimer | Par validateur à chaque refresh (20+ lignes toutes les 5 min) |
| `gnovalidator/valoper.go` | `✅ Fetched %d valopers from valopers.Render page %d` | Supprimer | Par page à chaque refresh ; la ligne de résumé en dessous suffit |
| `govdao/govdao.go` | `log.Println("Message sent:", string(message))` | Supprimer | Dump du payload WebSocket brut à chaque événement on-chain |
| `govdao/govdao.go` | `log.Printf("Title: %s", title)` | Supprimer | Debug print, le titre est déjà stocké en DB et envoyé en notification |
| `govdao/govdao.go` | `log.Printf("Block Height: %d", tx.BlockHeight)` | Supprimer | Debug print |
| `govdao/govdao.go` | `log.Printf("tx URL %s", txurl)` | Supprimer | Debug print |
| `govdao/govdao.go` | `log.Printf("ID: %d", idInt)` | Supprimer | Debug print |
| `govdao/govdao.go` | `log.Println(res)` dans `ExtractProposalRender` | Supprimer | Dump du rendu Gno Markdown brut (multi-ligne) en console à chaque vérification |
| `govdao/govdao.go` | `log.Printf("Blocks fetched: %+v\n", respData)` | Supprimer | Dump de la réponse GraphQL entière |

---

## Priorité 2 — Reformatages (logs utiles à conserver, format à corriger)

### `gnovalidator/gnovalidator_realtime.go`

| Log actuel | Log proposé | Raison |
|-----------|------------|--------|
| `⚠️ Database empty get last block: %v` | `[monitor][%s] no stored blocks, starting from genesis` | Message trompeur (`%v` affiche `<nil>`) ; pas de contexte chain |
| `❌ Failed to get latest block height: %v` | Supprimer (doublon avec le log dans le guard anti-spam juste en dessous) | Doublon : le guard `sinceRPCErr` logue déjà le même événement |
| `Error retrieving last block: %v` | `[monitor][%s] error fetching latest height: %v` | Pas de préfixe, casse incohérente |
| `⚠️ Impossible de récupérer la date du block %d: %v` (x2) | `[monitor][%s] cannot get block time for height %d: %v` | Texte en français, pas de contexte chain |
| `⏳ Backfill [%d..%d] (gap=%d)` | `[monitor][%s] backfill [%d..%d] (gap=%d)` | Manque contexte chain |
| `❌ backfill error: %v` | `[monitor][%s] backfill error: %v` | Manque contexte chain |
| `✅ Backfill done up to %d, switch to realtime` | `[monitor][%s] backfill complete up to %d, switching to realtime` | Manque contexte chain |
| `Erreur bloc %d: %v` | `[monitor][%s] error fetching block %d: %v` | Texte en français, pas de préfixe |
| `❌ Failed to save participation at height %d: %v` | `[monitor][%s] failed to save participation at height %d: %v` | Manque contexte chain |
| `🔁 Refresh MonikerMap...` | `[monitor][%s] refreshing moniker map` | Emoji inutile sur un log interne |
| `🚫 Too many alerte for %s, muting for 1h` | `[validator][%s] muting %s (%s) for 1h — too many alerts` | Typo "alerte", pas de contexte chain |

### `gnovalidator/Prometheus.go`

| Log actuel | Log proposé | Raison |
|-----------|------------|--------|
| `🔄 [%s] Starting metrics update...` | `[metrics][%s] updating` | Normalisation |
| `✅ [%s] All metrics updated` | `[metrics][%s] update complete` | Normalisation |
| `⚠️  [%s] TxContribution: all values are 0 — ...` | `[metrics][%s] TxContribution: all zero — proposer data may be missing` | Garder, normaliser préfixe |
| `PANIC in StartMetricsUpdater: %v` | `[metrics] panic recovered: %v` | Pas de crochets, pas de préfixe |
| `StartMetricsUpdater started. Enabled chains: %v` | `[metrics] started, chains: %v` | Normalisation |
| `TIMEOUT metrics update cycle exceeded %v, ...` | `[metrics] cycle timed out after %v, remaining chains skipped` | Casse incohérente |
| `ERROR [%s] metrics update: %v` | `[metrics][%s] update failed: %v` | Format préfixe incohérent |

### `gnovalidator/aggregator.go`

Supprimer uniquement les emojis, la structure `[aggregator][chain]` est déjà correcte.

| Log actuel | Log proposé |
|-----------|------------|
| `❌ [aggregator] panic recovered: %v` | `[aggregator] panic recovered: %v` |
| `⚠️  [aggregator] restarting after panic` | `[aggregator] restarting after panic` |
| `❌ [aggregator][%s] aggregation failed: %v` | `[aggregator][%s] aggregation failed: %v` |
| `❌ [aggregator][%s] prune failed: %v` | `[aggregator][%s] prune failed: %v` |
| `✅ [aggregator][%s] aggregated %d rows over %d days` | `[aggregator][%s] aggregated %d rows over %d days` |
| `🗑️  [aggregator][%s] pruned %d raw rows (> %d days old)` | `[aggregator][%s] pruned %d raw rows (older than %d days)` |

### `gnovalidator/valoper.go`

| Log actuel | Log proposé | Raison |
|-----------|------------|--------|
| `🎉 Total valopers fetched: %d\n` | `[valoper][%s] fetched %d valopers` | Supprimer emoji, ajouter chain |
| `✅ MonikerMap initialized with %d active validators\n` | `[valoper][%s] moniker map initialized: %d validators` | Normalisation |
| `✅ addr_monikers synced (%d entries)` | Supprimer (fusionner avec la ligne précédente) | Doublon immédiat avec la ligne ci-dessus |
| `⚠️ Failed to upsert addr_moniker %s: %v` | `[valoper][%s] failed to upsert moniker for %s: %v` | Manque contexte chain |
| `❌ Failed to retrieve validators after retries: %v` | `[valoper][%s] failed to retrieve validators after retries: %v` | Manque contexte chain |
| `🔁 Retry %d/%d after error: %v` | `[valoper] retry %d/%d: %v` | Supprimer emoji |

### `gnovalidator/gnovalidator_report.go`

| Log actuel | Log proposé | Raison |
|-----------|------------|--------|
| `🕓 Scheduled next report for %s at %s (%s)` | `[report] next for user %s at %s (in %s)` | Normalisation |
| `🕓 Scheduled next report for chat %d chain %s at %s (%s)` | `[report][%s] next for chat %d at %s (in %s)` | Normalisation |
| `⏰ Sending report for user %s` | `[report] sending for user %s` | Normalisation |
| `⏰ Sending report for chat %d chain %s` | `[report][%s] sending for chat %d` | Normalisation |
| `♻️ Reloading schedule for user %s` | `[report] reloading schedule for user %s` | Normalisation |
| `♻️ Reloading schedule for chat %d chain %s` | `[report][%s] reloading schedule for chat %d` | Normalisation |
| `[CalculateRate] Error querying participation: %v` | `[report][%s] error querying participation for %s: %v` | Normalisation |

### `database/db_metrics.go`

| Log actuel | Log proposé | Raison |
|-----------|------------|--------|
| `Error invalid period %s` (x2) | `[db] invalid period: %v` | Mauvais verbe (`%s` sur une `error`), pas de préfixe |
| `✅ Loaded %d monikers from DB` | `[db] loaded %d monikers` | Normalisation |
| `⚠️ createHourReport: %v` | `[db] createHourReport for user %s: %v` | Manque userID pour traçabilité |
| `Invalid timezone '%s', defaulting to UTC` | `[db] invalid timezone %q, defaulting to UTC` | Utiliser `%q` pour les strings avec caractères inattendus |

### `govdao/govdao.go`

| Log actuel | Log proposé | Raison |
|-----------|------------|--------|
| `Read error: ...` | `[govdao][%s] websocket read error: %v` | Manque contexte chain |
| `WebsocketGovdao dial error: %v — retrying in %s` | `[govdao][%s] dial error: %v — retrying in %s` | Manque contexte chain |
| `WebsocketGovdao lost connection — retrying in %s` | `[govdao][%s] connection lost — retrying in %s` | Manque contexte chain |
| `❌ WriteJSON initMsg: %v` | `[govdao][%s] send init message failed: %v` | Manque contexte chain |
| `Error fetch govdao %s` | `[govdao][%s] init fetch failed: %v` | Manque contexte chain, mauvais verbe |
| `✅ Proposal %d (%s) has been ACCEPTED!` | `[govdao][%s] proposal %d (%s) accepted` | Supprimer emoji et majuscules |
| `⏳ Checking proposal status...` | `[govdao] checking proposal statuses` | Normalisation |
| `Error fetching proposals: %v` | `[govdao] error fetching proposals: %v` | Manque préfixe |

### `telegram/validator.go`

| Log actuel | Log proposé | Raison |
|-----------|------------|--------|
| `error report activate%s` | `[telegram] report activate error: %v` | Espace manquant, mauvais verbe sur une `error` |
| `send %s failed: %v` dans handler `/report` | `[telegram] send /report failed: %v` | Copier-coller erroné : affiche "/missing" au lieu de "/report" |
| `send %s failed: %v` dans handler wildcard | `[telegram] send failed for unknown command chat=%d: %v` | Copier-coller erroné : affiche "/status" dans le fallback |
| `⚠️ UpdateChatChain chat_id=%d: %v` | `[telegram] UpdateChatChain chat=%d: %v` | Supprimer emoji |

### `main.go`

| Log actuel | Log proposé | Raison |
|-----------|------------|--------|
| `Starting monitoring for chain: %s` | `[main] starting monitoring for chain %s` | Normalisation |
| `Monitoring started for chain: %s` | Supprimer | Trompeur : la goroutine est lancée, pas confirmée démarrée |
| `❌ Failed to initialize database: %v` | `[main] failed to initialize database: %v` | Supprimer emoji |
| `✅ Database connection established successfully` | `[main] database ready` | Supprimer emoji, raccourcir |
| `Spawning monitoring loops for %d enabled chains: %v` | `[main] enabled chains (%d): %v` | Normalisation |
| `⚠️ Daily report scheduler disabled by flag` | `[main] daily report scheduler disabled` | Supprimer emoji |

---

## Problème architectural à corriger

`log.Fatalf` dans `ExtractTitle` et `ExtractProposalRender` dans `govdao/govdao.go` :
ces fonctions sont appelées depuis des goroutines. Un `log.Fatalf` sur une erreur RPC temporaire crash tout le processus. Ces fonctions doivent retourner l'erreur à l'appelant au lieu d'appeler `log.Fatalf`.

---

## Fichiers sans modification nécessaire

- `gnovalidator/sync.go` — aucun log
- `gnovalidator/metric.go` — aucun log
- `database/db.go` — aucun log
