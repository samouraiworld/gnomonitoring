# Feat Pagination (Telegram Validator)

## Goal
Add visual pagination for Telegram (validator) commands using **inline buttons** and a **stateless** design (no state stored in DB/memory).

## Decisions
- Pagination via **inline buttons** (Prev/Next) only.
- No `/next` `/prev` commands.
- No state table: all context is encoded in `callback_data`.
- Search via `filter=...` (already supported) — no "Search" interaction.

## Constraints
- `callback_data` limited to **64 bytes**.
- The bot must re-read data from the DB on each page.

## Action plan (detailed)
1. **List commands to paginate**
   - `status`, `uptime`, `operation_time`, `tx_contrib`, `missing`.

2. **Define a compact stateless `callback_data` format**
   - Goal: full context in 64 bytes.
   - Recommended short format: `c=uptime&p=2&l=10` (+ `period`/`filter` if present).
   - Rules:
     - `c` = command id (e.g. `status`, `uptime`, `optime`, `tx`, `miss`).
     - `p` = page (1-based).
     - `l` = limit.
     - `r` = period (abbreviated, e.g. `cm`, `cw`, `cy`, `all`).
     - `f` = filter (optional, abbreviated if needed).

3. **Add a Telegram handler for callbacks**
   - Intercept `callback_query`.
   - Parse `callback_data`.
   - Reconstruct a "virtual" request (cmd + params).
   - Reuse the same functions as text commands.

4. **Adapt formatting functions for pagination**
   - Introduce `offset` = `(page-1)*limit`.
   - Apply `limit/offset` after sorting.
   - Provide `total` to compute `Page X/Y`.

5. **Build the paginated message**
   - Header: `Page X/Y` and summary (e.g. period, filter).
   - Inline buttons:
     - `⬅️ Prev` if `page > 1`
     - `➡️ Next` if `page < totalPages`

6. **Edge cases**
   - Empty page -> go back to previous page.
   - `page < 1` -> force to 1.
   - `limit` > max -> clamp (e.g. 50).
   - `filter` too long -> truncate or encode.

7. **Search**
   - Keep `filter=...` (no interaction).
   - Ensure the filter is propagated in callbacks.

8. **Update help**
   - Indicate that results are paginated via buttons.

9. **Manual validation**
   - Simple command: `/uptime limit=5` then Next/Prev.
   - Command with period: `/status period=current_month`.
   - Command with filter: `/missing filter=...`.
   - Verify that pagination remains stable.

## Concrete changes (without code)
- Add a **callback_query handler** in `backend/internal/telegram/telegram.go`.
- Add an **inline keyboard builder** (Prev/Next buttons).
- Extend formatting functions in `backend/internal/telegram/validator.go` to accept `page/limit/offset` and return `total`.
- Potential SQL optimizations in `backend/internal/database/db_metrics.go` to handle sorting + pagination at DB level.

## Notes
- If `callback_data` ever becomes too long, switch to a DB-backed state store.
