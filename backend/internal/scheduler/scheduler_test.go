package scheduler

// White-box tests for the Scheduler type.  Because the reloadChans field and
// the key-construction logic inside StartForTelegram are unexported, tests live
// in the same package so they can inspect internal state directly.

import (
	"fmt"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newTestScheduler builds a Scheduler whose db field is set to the supplied
// *gorm.DB without calling StartAll / StartAllTelegram (which would require a
// live RPC endpoint).  This gives tests a clean, isolated instance.
func newTestScheduler(db *gorm.DB) *Scheduler {
	return &Scheduler{
		db:          db,
		reloadChans: make(map[string]chan struct{}),
	}
}

// stopAll closes every reload channel registered in the scheduler so that any
// goroutines started during the test exit promptly.
func stopAll(s *Scheduler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, ch := range s.reloadChans {
		close(ch)
		delete(s.reloadChans, key)
	}
}

// -------------------------------------------------------------------------
// TestSchedulerKeyIsolation
// -------------------------------------------------------------------------

// TestSchedulerKeyIsolation verifies that starting two Telegram schedules for
// the same chat ID on different chains creates two distinct entries in the
// internal reloadChans map and that the keys do not collide.
func TestSchedulerKeyIsolation(t *testing.T) {
	db := testoutils.NewTestDB(t)
	s := newTestScheduler(db)
	defer stopAll(s)

	const chatID int64 = 5001
	const chainA = "chainA"
	const chainB = "chainB"

	// Insert hour-report rows so that the goroutines spawned by StartForTelegram
	// have a valid DB record (the functions only use the reload channel, but we
	// keep the DB consistent for completeness).
	_, err := database.InsertChatID(db, chatID, "validator", chainA)
	require.NoError(t, err)
	_, err = database.InsertChatID(db, chatID, "validator", chainB)
	require.NoError(t, err)

	s.StartForTelegram(chatID, chainA, 9, 0, "UTC", db)
	s.StartForTelegram(chatID, chainB, 10, 30, "UTC", db)

	keyA := fmt.Sprintf("tg:%d:%s", chatID, chainA)
	keyB := fmt.Sprintf("tg:%d:%s", chatID, chainB)

	s.mu.Lock()
	chA, existsA := s.reloadChans[keyA]
	chB, existsB := s.reloadChans[keyB]
	s.mu.Unlock()

	assert.True(t, existsA, "reloadChans should contain key for chainA")
	assert.True(t, existsB, "reloadChans should contain key for chainB")
	assert.NotNil(t, chA, "reload channel for chainA must not be nil")
	assert.NotNil(t, chB, "reload channel for chainB must not be nil")

	// The two channels must be distinct objects — they guard independent goroutines.
	// Channels are compared by identity (same underlying pointer) using reflect.
	assert.NotEqual(t, fmt.Sprintf("%p", chA), fmt.Sprintf("%p", chB),
		"chainA and chainB must have separate reload channels")
}

// -------------------------------------------------------------------------
// TestSchedulerReloadForTelegramUpdatesKey
// -------------------------------------------------------------------------

// TestSchedulerReloadForTelegramUpdatesKey verifies that calling
// ReloadForTelegram replaces the existing reload channel for a (chat, chain)
// pair with a new one.  The old goroutine is stopped (its channel is closed)
// and a fresh goroutine is started with the updated schedule.
func TestSchedulerReloadForTelegramUpdatesKey(t *testing.T) {
	db := testoutils.NewTestDB(t)
	s := newTestScheduler(db)
	defer stopAll(s)

	const chatID int64 = 5002
	const chainID = "betanet"

	// Insert a telegram chat and its hour-report row.
	_, err := database.InsertChatID(db, chatID, "validator", chainID)
	require.NoError(t, err)

	// Start an initial schedule.
	s.StartForTelegram(chatID, chainID, 8, 0, "UTC", db)

	key := fmt.Sprintf("tg:%d:%s", chatID, chainID)

	s.mu.Lock()
	firstCh, exists := s.reloadChans[key]
	s.mu.Unlock()

	require.True(t, exists, "initial StartForTelegram should register a reload channel")
	require.NotNil(t, firstCh)

	// Update the schedule time in the DB so ReloadForTelegram has new values to
	// pick up.
	err = database.UpdateTelegramHeureReport(db, 18, 30, "Europe/Paris", chatID, chainID)
	require.NoError(t, err)

	// Reload — this should close firstCh and register a brand-new channel.
	err = s.ReloadForTelegram(chatID, chainID, db)
	require.NoError(t, err)

	s.mu.Lock()
	secondCh, exists := s.reloadChans[key]
	s.mu.Unlock()

	require.True(t, exists, "ReloadForTelegram should keep the key registered")
	require.NotNil(t, secondCh)

	// The channel must have been replaced — not the same underlying pointer.
	assert.NotEqual(t, fmt.Sprintf("%p", firstCh), fmt.Sprintf("%p", secondCh),
		"ReloadForTelegram should replace the reload channel with a new one")

	// The old channel must be closed (reading from a closed channel returns
	// immediately with the zero value).
	select {
	case _, open := <-firstCh:
		assert.False(t, open, "the original reload channel should be closed after reload")
	default:
		// If the select falls through to default the channel is still open,
		// which means StartForTelegram did not close it — that is a failure.
		t.Error("original reload channel was not closed by ReloadForTelegram")
	}
}
