package gnovalidator

import (
	"log"
	"sync"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

type dpRow struct {
	ChainID        string
	Date           time.Time
	BlockHeight    int64
	Moniker        string
	Addr           string
	Participated   bool
	TxContribution bool
	Proposed       bool
}

type job struct{ H int64 }
type out struct {
	Rows []dpRow
	Err  error
}

// RecordActivationOrSkip is the shared first-activation guard used while
// writing daily_participations rows. It reads and writes the live,
// thread-safe FirstActiveBlockMap (GetFirstActiveBlock/SetFirstActiveBlockIfEarlier)
// rather than a point-in-time snapshot, so concurrent backfill workers
// observe each other's discoveries immediately instead of each working off
// a copy that's stale for the whole run. Returns true when the caller
// should skip writing a row for this (chainID, addr, height) because it
// predates the validator's first recorded activation.
func RecordActivationOrSkip(db *gorm.DB, chainID, addr string, height int64, participated bool) bool {
	if participated {
		// SetFirstActiveBlockIfEarlier is a single atomic check-and-write, so
		// with 20 concurrent workers processing blocks without a guaranteed
		// ascending-height order, whichever true-participation height turns out
		// to be the smallest always wins as the recorded activation — never a
		// later height that merely happened to be processed first.
		if SetFirstActiveBlockIfEarlier(chainID, addr, height) {
			_ = database.UpsertFirstActiveBlock(db, chainID, addr, height)
		}
		return false
	}
	fab := GetFirstActiveBlock(chainID, addr)
	if fab == -1 {
		// Return true (skip) when activation is still completely unknown.
		// This closes the phantom pre-activation row bug: with 20 concurrent workers
		// pulling jobs without strict ordering, a validator's true first-activation block
		// can be discovered by any worker at any time relative to other blocks in flight.
		// Writing a row when fab is unknown would allow phantom pre-activation rows for
		// any block processed before some worker discovers the real activation —
		// which is the exact bug this function exists to fix. The trade-off is narrow:
		// a handful of blocks right around the true activation boundary might be silently
		// skipped (not recorded as a genuine miss) if processed before that worker's
		// discovery reaches the map. That's a bounded, rare undercount very close to
		// onboarding — much less harmful than unbounded phantom pre-activation rows for
		// the validator's entire pre-existence.
		return true
	}
	if fab > 0 && height < fab {
		return true
	}
	return false
}

// for not send insert very longue trunc dpRow
func flushBatch(db *gorm.DB, rows []dpRow) error {
	// if dprow empty stop
	if len(rows) == 0 {
		return nil
	}
	const cols = 8 // chain_id, date, block_height, moniker, addr, participated, tx_contribution, proposed
	const maxVars = 30_000 // Postgres supports up to 65535 bind parameters per statement; stay well below.
	maxRows := maxVars / cols // = 3750 rows per INSERT

	for start := 0; start < len(rows); start += maxRows {
		end := start + maxRows
		if end > len(rows) {
			end = len(rows)
		}
		if err := flushChunk(db, rows[start:end]); err != nil {
			return err
		}
	}
	return nil
}
func flushChunk(db *gorm.DB, rows []dpRow) error {
	if len(rows) == 0 {
		return nil
	}

	q := `
      INSERT INTO daily_participations
        (chain_id, date, block_height, moniker, addr, participated, tx_contribution, proposed)
      VALUES `
	args := make([]any, 0, len(rows)*8)
	for i, r := range rows {
		if i > 0 {
			q += ","
		}
		q += "(?, ?, ?, ?, ?, ?, ?, ?)"
		args = append(args, r.ChainID, r.Date, r.BlockHeight, r.Moniker, r.Addr, r.Participated, r.TxContribution, r.Proposed)
	}
	q += `
	  ON CONFLICT(chain_id, block_height, addr) DO UPDATE SET
	    date = excluded.date,
	    moniker = excluded.moniker,
	    participated = excluded.participated,
	    tx_contribution = excluded.tx_contribution,
	    proposed = excluded.proposed
	`

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(q, args...).Error; err != nil {
			return err
		}
		return nil
	})
}

// sequentielle 42hours approx for 1 month
func BackfillRange(db *gorm.DB, client gnoclient.Client, chainID string, from, to int64, monikerMap map[string]string) error {
	const chunk = int64(1000)   // number of blocks per tranche
	const flushThreshold = 3000 // number row before row flush

	firstActiveBlocks := GetFirstActiveBlockMap(chainID)
	buf := make([]dpRow, 0, flushThreshold)

	for start := from + 1; start <= to; start += chunk {
		end := start + chunk - 1
		if end > to {
			end = to
		}

		// Sequential download
		for h := start; h <= end; h++ {
			block, err := client.Block(h)
			if err != nil || block == nil || block.Block == nil || block.Block.LastCommit == nil {
				continue
			}

			// Actual block proposer, resolved once.
			proposerAddr := block.Block.Header.ProposerAddress.String()
			hasTx := len(block.Block.Data.Txs) > 0
			timeStp := block.Block.Header.Time

			precommitAddrs := make([]string, 0, len(block.Block.LastCommit.Precommits))
			for _, precommit := range block.Block.LastCommit.Precommits {
				if precommit != nil {
					precommitAddrs = append(precommitAddrs, precommit.ValidatorAddress.String())
				}
			}
			participating := buildParticipation(precommitAddrs, proposerAddr, hasTx, timeStp)
			for valAddr, moniker := range monikerMap {
				participated := participating[valAddr] // false if not found

				if participated.Participated {
					// Dynamic detection: record first_active_block when first seen
					if fab := firstActiveBlocks[valAddr]; fab == -1 {
						firstActiveBlocks[valAddr] = h
						SetFirstActiveBlock(chainID, valAddr, h)
						_ = database.UpsertFirstActiveBlock(db, chainID, valAddr, h)
					}
				} else {
					// Guard: skip rows before the validator's activation block
					if fab := firstActiveBlocks[valAddr]; fab > 0 && h < fab {
						continue
					}
				}

				buf = append(buf, dpRow{
					ChainID:        chainID,
					Date:           timeStp,
					BlockHeight:    h,
					Moniker:        moniker,
					Addr:           valAddr,
					Participated:   participated.Participated,
					TxContribution: participated.TxContribution,
					Proposed:       participated.Proposed,
				})
				if len(buf) >= flushThreshold {
					if err := flushBatch(db, buf); err != nil {
						return err
					}
					// empty the buffer
					buf = buf[:0]
				}

			}

		}
		// Call flushBatch at the end to write the remaining rows if the flush threshold wasn’t reached.
		if err := flushBatch(db, buf); err != nil {
			return err
		}
		buf = buf[:0]
	}
	return nil
}

// Parallel
// - 5 approx hours with 6 workers for one month
// - 2 approx  hours with 20 workers for one month
func BackfillParallel(db *gorm.DB, client gnoclient.Client, chainID string, from, to int64, monikerMap map[string]string) error {
	const workers = 20
	const flushThreshold = 2000

	jobs := make(chan job, 2048)
	outs := make(chan out, 2048)

	// workers RPC
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				b, err := client.Block(j.H)
				if err != nil || b == nil || b.Block == nil || b.Block.LastCommit == nil {
					outs <- out{Err: err}
					continue
				}
				proposerAddr := b.Block.Header.ProposerAddress.String()
				hasTx := len(b.Block.Data.Txs) > 0
				tStr := b.Block.Header.Time

				precommitAddrs := make([]string, 0, len(b.Block.LastCommit.Precommits))
				for _, pc := range b.Block.LastCommit.Precommits {
					if pc != nil {
						precommitAddrs = append(precommitAddrs, pc.ValidatorAddress.String())
					}
				}
				participating := buildParticipation(precommitAddrs, proposerAddr, hasTx, tStr)

				rows := make([]dpRow, 0, len(monikerMap))
				for addr, mon := range monikerMap {
					p := participating[addr] // zero value (not participated/proposed) if absent
					if RecordActivationOrSkip(db, chainID, addr, j.H, p.Participated) {
						continue
					}
					rows = append(rows, dpRow{
						ChainID:        chainID,
						Date:           tStr,
						BlockHeight:    j.H,
						Moniker:        mon,
						Addr:           addr,
						Participated:   p.Participated,
						TxContribution: p.TxContribution,
						Proposed:       p.Proposed,
					})
				}
				outs <- out{Rows: rows}
			}
		}()
	}

	// closer
	go func() { wg.Wait(); close(outs) }()

	// producer
	go func() {
		for h := from; h <= to; h++ {
			jobs <- job{H: h}
		}
		close(jobs)
	}()

	// writer unique + batch
	buf := make([]dpRow, 0, flushThreshold)
	var minDate, maxDate time.Time
	for o := range outs {
		if o.Err != nil {
			continue
		}
		buf = append(buf, o.Rows...)
		for _, r := range o.Rows {
			if minDate.IsZero() || r.Date.Before(minDate) {
				minDate = r.Date
			}
			if r.Date.After(maxDate) {
				maxDate = r.Date
			}
		}
		if len(buf) >= flushThreshold {
			if err := flushBatch(db, buf); err != nil {
				return err
			}
			buf = buf[:0]
		}
	}
	if err := flushBatch(db, buf); err != nil {
		return err
	}

	// The backfilled range can span days already rolled into
	// daily_participation_agregas (this runs whenever the live sync gap
	// exceeds 500 blocks, not just at startup — see the "> 500" check in
	// CollectParticipation). AggregateChain never revisits an already-
	// aggregated day, so force a re-aggregation pass over exactly the days
	// this backfill touched.
	if !minDate.IsZero() {
		if err := ReaggregateDateRange(db, chainID, minDate, maxDate); err != nil {
			log.Printf("[monitor][%s] backfill: re-aggregate [%s..%s] failed: %v",
				chainID, minDate.Format("2006-01-02"), maxDate.Format("2006-01-02"), err)
		}
	}
	return nil
}
