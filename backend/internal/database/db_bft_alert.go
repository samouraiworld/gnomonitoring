package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// BFTWindowEntry is one current valset member (voting_power > 0 in the
// latest addr_monikers snapshot) with its signing record over the last N
// blocks of the chain. Blocks is the number of rows it has in the window
// (one per block it was in the valset for); Signed is how many of them it
// signed. A member with no row in the window has Blocks = Signed = 0.
type BFTWindowEntry struct {
	Addr        string `gorm:"column:addr"`
	Moniker     string `gorm:"column:moniker"`
	VotingPower int64  `gorm:"column:voting_power"`
	Blocks      int64  `gorm:"column:blocks"`
	Signed      int64  `gorm:"column:signed"`
}

// BFTWindow is the result of GetBFTWindow: the window bounds, how many of
// its heights were actually collected (Heights; a fetch failure leaves a
// gap until gap reconciliation fills it), the block time of the highest
// height, and one entry per voting-power holder. MaxHeight is 0 when the
// chain has no participation row yet.
type BFTWindow struct {
	MinHeight     int64
	MaxHeight     int64
	Heights       int64
	LastBlockTime time.Time
	Entries       []BFTWindowEntry
}

// GetBFTWindow returns, for every validator holding voting power in the
// latest snapshot, how many of the chain's last windowBlocks blocks it
// signed. Scoped to chain_id. The window is anchored on the highest height
// stored for the chain, so a stalled chain keeps reporting the last blocks
// it produced (a halt is the stagnation alert's job, not this one's).
func GetBFTWindow(db *gorm.DB, chainID string, windowBlocks int) (BFTWindow, error) {
	var w BFTWindow
	if err := db.Raw(`SELECT COALESCE(MAX(block_height), 0) FROM daily_participations WHERE chain_id = ?`,
		chainID).Scan(&w.MaxHeight).Error; err != nil {
		return BFTWindow{}, fmt.Errorf("GetBFTWindow(%s) max height: %w", chainID, err)
	}
	if w.MaxHeight == 0 {
		return w, nil
	}
	w.MinHeight = w.MaxHeight - int64(windowBlocks) + 1
	if w.MinHeight < 1 {
		w.MinHeight = 1
	}

	var cov struct {
		Heights  int64
		LastTime *time.Time
	}
	if err := db.Raw(`
		SELECT COUNT(DISTINCT block_height) AS heights,
		       MAX(date) FILTER (WHERE block_height = ?) AS last_time
		FROM daily_participations
		WHERE chain_id = ? AND block_height BETWEEN ? AND ?`,
		w.MaxHeight, chainID, w.MinHeight, w.MaxHeight).Scan(&cov).Error; err != nil {
		return BFTWindow{}, fmt.Errorf("GetBFTWindow(%s) coverage: %w", chainID, err)
	}
	w.Heights = cov.Heights
	if cov.LastTime != nil {
		w.LastBlockTime = *cov.LastTime
	}

	if err := db.Raw(`
		WITH win AS (
			SELECT addr,
			       COUNT(*) AS blocks,
			       COUNT(*) FILTER (WHERE participated) AS signed
			FROM daily_participations
			WHERE chain_id = ? AND block_height BETWEEN ? AND ?
			GROUP BY addr
		)
		SELECT am.addr, am.moniker, am.voting_power,
		       COALESCE(win.blocks, 0) AS blocks,
		       COALESCE(win.signed, 0) AS signed
		FROM addr_monikers am
		LEFT JOIN win ON win.addr = am.addr
		WHERE am.chain_id = ? AND am.voting_power > 0
		ORDER BY am.voting_power DESC, am.addr`,
		chainID, w.MinHeight, w.MaxHeight, chainID).Scan(&w.Entries).Error; err != nil {
		return BFTWindow{}, fmt.Errorf("GetBFTWindow(%s): %w", chainID, err)
	}
	return w, nil
}

// BFTAlertState is the most recent BFT-margin alert_logs row of a chain.
type BFTAlertState struct {
	Level  string    `gorm:"column:level"`
	SentAt time.Time `gorm:"column:sent_at"`
}

// GetBFTAlertState returns the chain's latest BFT_WARNING, BFT_CRITICAL or
// BFT_RESOLVED row, or a zero state (Level "") when none exists. The levels
// are dedicated to this alert, so no other alert family can mask it.
// Scoped to chain_id.
func GetBFTAlertState(db *gorm.DB, chainID string) (BFTAlertState, error) {
	var rows []BFTAlertState
	if err := db.Raw(`
		SELECT level, sent_at
		FROM alert_logs
		WHERE chain_id = ? AND addr = 'bft'
		  AND level IN ('BFT_WARNING', 'BFT_CRITICAL', 'BFT_RESOLVED')
		ORDER BY sent_at DESC, id DESC
		LIMIT 1`, chainID).Scan(&rows).Error; err != nil {
		return BFTAlertState{}, fmt.Errorf("GetBFTAlertState(%s): %w", chainID, err)
	}
	if len(rows) == 0 {
		return BFTAlertState{}, nil
	}
	return rows[0], nil
}
