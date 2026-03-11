# Feat Pagination (Telegram Validator)

## Objectif
Ajouter une pagination visuelle pour les commandes Telegram (validator) avec **inline buttons** et un design **stateless** (aucun stockage d’état en DB/mémoire).

## Décisions prises
- Pagination via **inline buttons** (Prev/Next) uniquement.
- Pas de commandes `/next` `/prev`.
- Pas de table d’état: tout le contexte est encodé dans `callback_data`.
- Recherche via `filter=...` (déjà possible) — pas d’interaction “Search”.

## Contraintes
- `callback_data` limité à **64 bytes**.
- Le bot doit re-lire les données en DB à chaque page.

## Plan d’action (détaillé)
1. **Lister les commandes à paginer**
   - `status`, `uptime`, `operation_time`, `tx_contrib`, `missing`.

2. **Définir un format compact de `callback_data` (stateless)**
   - Objectif: tout le contexte dans 64 bytes.
   - Format court recommandé: `c=uptime&p=2&l=10` (+ `period`/`filter` si présent).
   - Règles:
     - `c` = command id (ex: `status`, `uptime`, `optime`, `tx`, `miss`).
     - `p` = page (1-based).
     - `l` = limit.
     - `r` = period (abrégé, ex: `cm`, `cw`, `cy`, `all`).
     - `f` = filter (optionnel, version abrégée si nécessaire).

3. **Ajouter un handler Telegram pour les callbacks**
   - Intercepter `callback_query`.
   - Parser `callback_data`.
   - Recomposer une requête “virtuelle” (cmd + params).
   - Réutiliser les mêmes fonctions que les commandes texte.

4. **Adapter les fonctions de formatage pour pagination**
   - Introduire `offset` = `(page-1)*limit`.
   - Appliquer `limit/offset` après tri.
   - Prévoir le `total` pour calculer `Page X/Y`.

5. **Construire le message paginé**
   - En-tête: `Page X/Y` et résumé (ex: période, filtre).
   - Inline buttons:
     - `⬅️ Prev` si `page > 1`
     - `➡️ Next` si `page < totalPages`

6. **Edge cases**
   - Page vide -> revenir page précédente.
   - `page < 1` -> forcer à 1.
   - `limit` > max -> clamp (ex: 50).
   - `filter` trop long -> tronquer ou encoder.

7. **Recherche**
   - Conserver `filter=...` (pas d’interaction).
   - S’assurer que le filtre est propagé dans les callbacks.

8. **Mise à jour du help**
   - Indiquer que les résultats sont paginés via boutons.

9. **Validation manuelle**
   - Commande simple: `/uptime limit=5` puis Next/Prev.
   - Commande avec période: `/status period=current_month`.
   - Commande avec filtre: `/missing filter=...`.
   - Vérifier que la pagination reste stable.

## Changements concrets à prévoir (sans code)
- Ajout d’un **handler callback_query** dans `backend/internal/telegram/telegram.go`.
- Ajout d’un **constructeur d’inline keyboard** (boutons Prev/Next).
- Extension des fonctions de formatage dans `backend/internal/telegram/validator.go` pour accepter `page/limit/offset` et retourner `total`.
- Éventuelles optimisations SQL dans `backend/internal/database/db_metrics.go` pour gérer tri + pagination côté DB.

## Notes
- Si un jour le `callback_data` devient trop long, basculer vers un stockage d’état en DB.
