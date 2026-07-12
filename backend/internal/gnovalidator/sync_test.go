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
