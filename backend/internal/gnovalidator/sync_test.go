package gnovalidator_test

import (
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/require"
)

func TestRecordActivationOrSkip(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chainID := "test-record-activation"
	addr := "g1neveractivated"

	// Before any true activation is ever recorded, a participated=false row
	// must be skipped (this is the guard that stops phantom pre-join history).
	require.True(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 10, false))

	// A true participation at height 30 records the activation and must
	// never itself be skipped.
	require.False(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 30, true))
	require.Equal(t, int64(30), gnovalidator.GetFirstActiveBlock(chainID, addr))

	// A height BEFORE the recorded activation, evaluated AFTER the
	// activation was recorded (simulating an out-of-order concurrent
	// backfill worker), is now correctly skipped — this is the exact
	// scenario the old frozen-snapshot code got wrong.
	require.True(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 15, false))

	// A height AFTER activation that still didn't participate is a real
	// miss and must NOT be skipped.
	require.False(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 31, false))
}

func TestRecordActivationOrSkip_OutOfOrderKeepsEarliestActivation(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chainID := "test-record-activation-race"
	addr := "g1raced"

	// A worker processing a LATER height's true participation runs first —
	// plausible with 20 concurrent BackfillParallel workers pulling jobs
	// without a guaranteed ascending-height order.
	require.False(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 110, true))
	require.Equal(t, int64(110), gnovalidator.GetFirstActiveBlock(chainID, addr))

	// A worker processing the true, EARLIER first-activation height arrives
	// after. It must lower the recorded value, not be discarded just because
	// a later height already "won" the race.
	require.False(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 100, true))
	require.Equal(t, int64(100), gnovalidator.GetFirstActiveBlock(chainID, addr))

	// A real miss between the true activation (100) and the wrongly-raced
	// later value (110) must now be recorded, not silently discarded as
	// "before activation" — this was the exact undercount the non-atomic
	// check-then-act version of this guard was exposed to.
	require.False(t, gnovalidator.RecordActivationOrSkip(db, chainID, addr, 105, false))
}
