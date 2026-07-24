package gnovalidator

import (
	"testing"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/gnolang/gno/tm2/pkg/bft/types"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func blockWithPrecommits(height int64, ts time.Time, signers ...types.Address) *ctypes.ResultBlock {
	precommits := make([]*types.CommitSig, 0, len(signers))
	for _, a := range signers {
		cs := types.CommitSig(types.Vote{ValidatorAddress: a})
		precommits = append(precommits, &cs)
	}
	return &ctypes.ResultBlock{
		Block: &types.Block{
			Header:     types.Header{Height: height, Time: ts},
			LastCommit: &types.Commit{Precommits: precommits},
		},
	}
}

func TestBackfillHeights_WritesOnlyRequestedNonContiguousHeights(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chainID := "backfillheightstest"

	var validatorAddr types.Address
	validatorAddr[0] = 0x42
	addrStr := validatorAddr.String()

	// Validator activated at height 200 (in-memory guard so a
	// participated=false row at 203 is recorded as a real miss, not skipped
	// as pre-activation phantom history).
	SetFirstActiveBlock(chainID, addrStr, 200)

	day := time.Now().UTC().AddDate(0, 0, -3)
	ts := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, time.UTC)

	fake := &fakeRPCClient{blockFunc: func(height int64) (*ctypes.ResultBlock, error) {
		switch height {
		case 201:
			return blockWithPrecommits(height, ts, validatorAddr), nil // signed
		case 203:
			return blockWithPrecommits(height, ts), nil // no precommits: missed
		default:
			t.Fatalf("unexpected block fetch for height %d (202 must never be requested)", height)
			return nil, nil
		}
	}}
	client := gnoclient.Client{RPCClient: fake}

	monikerMap := map[string]string{addrStr: "validator-a"}
	if err := BackfillHeights(db, client, chainID, []int64{201, 203}, monikerMap); err != nil {
		t.Fatalf("BackfillHeights error: %v", err)
	}

	rows := []database.DailyParticipation{}
	if err := db.Raw(`
		SELECT block_height, participated FROM daily_participations
		WHERE chain_id = ? AND addr = ? ORDER BY block_height
	`, chainID, addrStr).Scan(&rows).Error; err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (heights 201 and 203 only)", len(rows))
	}
	if rows[0].BlockHeight != 201 || !rows[0].Participated {
		t.Fatalf("height 201 row = %+v, want participated=true", rows[0])
	}
	if rows[1].BlockHeight != 203 || rows[1].Participated {
		t.Fatalf("height 203 row = %+v, want participated=false", rows[1])
	}

	var totalBlocks int
	if err := db.Raw(`
		SELECT total_blocks FROM daily_participation_agregas WHERE chain_id = ? AND addr = ?
	`, chainID, addrStr).Scan(&totalBlocks).Error; err != nil {
		t.Fatalf("query agrega: %v", err)
	}
	if totalBlocks != 2 {
		t.Fatalf("got total_blocks=%d after re-aggregation, want 2 (BackfillHeights must trigger ReaggregateDateRange)", totalBlocks)
	}
}
