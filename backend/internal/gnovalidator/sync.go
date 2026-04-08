package gnovalidator

import (
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
}

type job struct{ H int64 }
type out struct {
	Rows []dpRow
	Err  error
}

// for not send insert very longue trunc dpRow
func flushBatch(db *gorm.DB, rows []dpRow) error {
	// if dprow empty stop
	if len(rows) == 0 {
		return nil
	}
	const cols = 7
	const maxVars = 990 // marge
	// sqlite limit insert 999 var
	maxRows := maxVars / cols
	// len(rows) nbr rows

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
        (chain_id, date, block_height, moniker, addr, participated, tx_contribution)
      VALUES `
	args := make([]any, 0, len(rows)*7)
	for i, r := range rows {
		if i > 0 {
			q += ","
		}
		q += "(?, ?, ?, ?, ?, ?, ?)"
		args = append(args, r.ChainID, r.Date, r.BlockHeight, r.Moniker, r.Addr, r.Participated, r.TxContribution)
	}
	q += `
	  ON CONFLICT(chain_id, block_height, addr) DO UPDATE SET
	    date = excluded.date,
	    moniker = excluded.moniker,
	    participated = excluded.participated,
	    tx_contribution = excluded.tx_contribution
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

			// == IF in json return section Data, have a tx and get proposer of tx
			var txProposer string
			if len(block.Block.Data.Txs) > 0 {
				txProposer = block.Block.Header.ProposerAddress.String()

			}
			// === Get Timestamp ==

			timeStp := block.Block.Header.Time
			participating := make(map[string]Participation)
			for _, precommit := range block.Block.LastCommit.Precommits {
				if precommit != nil {
					var tx bool

					if precommit.ValidatorAddress.String() == txProposer {
						tx = true
					} else {
						tx = false
					}

					participating[precommit.ValidatorAddress.String()] = Participation{
						Participated:   true,
						Timestamp:      timeStp,
						TxContribution: tx,
					}

				}
			}
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

	firstActiveBlocks := GetFirstActiveBlockMap(chainID)
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
				hasTx := len(b.Block.Data.Txs) > 0
				var txProp string
				if hasTx {
					txProp = b.Block.Header.ProposerAddress.String()
				}

				tStr := b.Block.Header.Time

				seen := make(map[string]struct{}, len(b.Block.LastCommit.Precommits))
				rows := make([]dpRow, 0, len(monikerMap))

				// participated true
				for _, pc := range b.Block.LastCommit.Precommits {
					if pc == nil {
						continue
					}
					addr := pc.ValidatorAddress.String()
					seen[addr] = struct{}{}
					// Dynamic detection: record first_active_block when first seen
					if fab := firstActiveBlocks[addr]; fab == -1 {
						SetFirstActiveBlock(chainID, addr, j.H)
						_ = database.UpsertFirstActiveBlock(db, chainID, addr, j.H)
					}
					rows = append(rows, dpRow{
						ChainID:        chainID,
						Date:           tStr,
						BlockHeight:    j.H,
						Moniker:        monikerMap[addr],
						Addr:           addr,
						Participated:   true,
						TxContribution: hasTx && (addr == txProp),
					})
				}
				// participated false
				for addr, mon := range monikerMap {
					if _, ok := seen[addr]; ok {
						continue
					}
					// Guard: skip rows before the validator's activation block
					if fab := firstActiveBlocks[addr]; fab > 0 && j.H < fab {
						continue
					}
					rows = append(rows, dpRow{
						ChainID:        chainID,
						Date:           tStr,
						BlockHeight:    j.H,
						Moniker:        mon,
						Addr:           addr,
						Participated:   false,
						TxContribution: false,
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
	for o := range outs {
		if o.Err != nil {
			continue
		}
		buf = append(buf, o.Rows...)
		if len(buf) >= flushThreshold {
			if err := flushBatch(db, buf); err != nil {
				return err
			}
			buf = buf[:0]
		}
	}
	return flushBatch(db, buf)
}
