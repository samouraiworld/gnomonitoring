package gnovalidator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
	"github.com/stretchr/testify/assert"
)

func TestShouldSendRPCAlert_FirstOutageAlwaysAlerts(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
}

func TestShouldSendRPCAlert_SuppressedInsideCooldown(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
	assert.False(t, shouldSendRPCAlert("chain-a", now.Add(9*time.Minute), 10*time.Minute))
	assert.True(t, shouldSendRPCAlert("chain-a", now.Add(11*time.Minute), 10*time.Minute))
}

func TestShouldSendRPCAlert_IsPerChain(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
	assert.True(t, shouldSendRPCAlert("chain-b", now, 10*time.Minute),
		"one chain's RPC outage must not mute another chain's alert")
}

// TestShouldSendRPCResolved_PairsWithAnAnnouncedCritical replaces the old
// TestClearRPCAlert_ReArmsTheGate: a RESOLVED may only fire once, for the
// CRITICAL it is paired with — a second resolve for the same outage (nothing
// new got announced in between) must be treated as an orphan and suppressed.
func TestShouldSendRPCResolved_PairsWithAnAnnouncedCritical(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	// No outage was ever announced: nothing to resolve.
	assert.False(t, shouldSendRPCResolved("chain-a"))

	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
	assert.True(t, shouldSendRPCResolved("chain-a"), "a dispatched CRITICAL must find its pair")
	assert.False(t, shouldSendRPCResolved("chain-a"), "a second resolve with nothing newly announced is an orphan")
}

// TestRPCAlertCooldown_SurvivesARecovery replaces the old
// TestClearRPCAlert_ReArmsTheGate, whose assertion ("a recovery must let the
// next outage alert immediately") was itself the bug: the pool always pairs
// one EventAllDown with exactly one later EventRecovered (see
// rpcpool.Client.onAllDown/onSuccess), so if a RESOLVED unconditionally
// re-armed the cooldown gate, a flapping endpoint would alert on every single
// flap. A RESOLVED must not reset the cooldown clock.
func TestRPCAlertCooldown_SurvivesARecovery(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
	assert.True(t, shouldSendRPCResolved("chain-a"))

	// Still inside the cooldown window opened by the first CRITICAL: a flap
	// one minute later must not alert again.
	assert.False(t, shouldSendRPCAlert("chain-a", now.Add(time.Minute), 10*time.Minute))

	// Once the cooldown has genuinely elapsed, a new outage may alert.
	assert.True(t, shouldSendRPCAlert("chain-a", now.Add(11*time.Minute), 10*time.Minute))
}

// TestRPCAlertFlapping_OneCriticalPerCooldownWindowNoOrphans is the flapping
// scenario from the bug report: an endpoint alternating down/up every
// health-check tick must not spam one CRITICAL+RESOLVED pair per flap. Across
// several flap cycles inside a single cooldown window, at most one CRITICAL
// may be dispatched, and every dispatched CRITICAL must have exactly one
// paired RESOLVED — no orphan RESOLVED for a suppressed CRITICAL, and no
// CRITICAL left forever unresolved.
func TestRPCAlertFlapping_OneCriticalPerCooldownWindowNoOrphans(t *testing.T) {
	resetRPCAlertState()
	base := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	cooldown := 10 * time.Minute

	criticalsSent := 0
	resolvedSent := 0
	// Six flap cycles, one minute apart: all but the last fall inside the
	// same 10-minute cooldown window opened by the first.
	for i := 0; i < 6; i++ {
		tick := base.Add(time.Duration(i) * time.Minute)
		if shouldSendRPCAlert("chain-a", tick, cooldown) {
			criticalsSent++
			assert.True(t, shouldSendRPCResolved("chain-a"), "a dispatched CRITICAL must always find its pair")
			resolvedSent++
		} else {
			assert.False(t, shouldSendRPCResolved("chain-a"), "a suppressed CRITICAL must never pair with a dispatched RESOLVED")
		}
	}
	assert.Equal(t, 1, criticalsSent, "at most one CRITICAL per cooldown window")
	assert.Equal(t, criticalsSent, resolvedSent, "every dispatched CRITICAL must have exactly one paired RESOLVED — no orphans")

	// A flap after the cooldown has elapsed alerts again, and still pairs.
	later := base.Add(11 * time.Minute)
	assert.True(t, shouldSendRPCAlert("chain-a", later, cooldown))
	assert.True(t, shouldSendRPCResolved("chain-a"))
}

// TestUndoRPCAlertDispatch_ClearsPhantomAnnouncement covers the dispatch-queue-full
// path: a CRITICAL that shouldSendRPCAlert cleared but that never actually
// reached the wire (the worker's queue was full) must not leave a phantom
// "announced" outage waiting for a RESOLVED that will never come, and must
// not block a genuine retry behind a cooldown for an alert nobody received.
func TestUndoRPCAlertDispatch_ClearsPhantomAnnouncement(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
	undoRPCAlertDispatch("chain-a")

	assert.False(t, shouldSendRPCResolved("chain-a"), "rollback must clear the announcement: nothing to resolve")
	assert.True(t, shouldSendRPCAlert("chain-a", now.Add(time.Second), 10*time.Minute),
		"rollback must not leave a phantom cooldown blocking a genuine retry")
}

// setRPCDispatchForTest overrides the package-level dispatch seam and
// restores the default on test cleanup, so NewRPCObserver's event handling
// can be exercised without doing real webhook/DB I/O.
func setRPCDispatchForTest(t *testing.T, fn func(rpcJob)) {
	t.Helper()
	setRPCDispatch(fn)
	t.Cleanup(func() { setRPCDispatch(defaultRPCDispatch) })
}

func TestNewRPCObserver_EventRotatedDispatchesNothing(t *testing.T) {
	resetRPCAlertState()
	dispatched := make(chan rpcJob, 4)
	setRPCDispatchForTest(t, func(job rpcJob) { dispatched <- job })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs := NewRPCObserver(ctx, nil, "chain-a")

	obs(rpcpool.EventRotated, "http://b", nil)

	select {
	case job := <-dispatched:
		t.Fatalf("EventRotated must not dispatch anything, got %+v", job)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNewRPCObserver_AllDownThenRecovered_DispatchesPairedAlerts(t *testing.T) {
	resetRPCAlertState()
	dispatched := make(chan rpcJob, 4)
	setRPCDispatchForTest(t, func(job rpcJob) { dispatched <- job })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs := NewRPCObserver(ctx, nil, "chain-a")

	obs(rpcpool.EventAllDown, "http://a", errors.New("boom"))
	select {
	case job := <-dispatched:
		assert.Equal(t, "CRITICAL", job.level)
		assert.Equal(t, "chain-a", job.chainID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CRITICAL dispatch")
	}

	obs(rpcpool.EventRecovered, "http://a", nil)
	select {
	case job := <-dispatched:
		assert.Equal(t, "RESOLVED", job.level)
		assert.Equal(t, "chain-a", job.chainID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RESOLVED dispatch")
	}
}

// TestNewRPCObserver_RecoveredWithoutAnnouncedOutage_DispatchesNothing covers
// a stray EventRecovered with no preceding announced CRITICAL (e.g. it was
// suppressed by cooldown): it must not produce an orphan RESOLVED.
func TestNewRPCObserver_RecoveredWithoutAnnouncedOutage_DispatchesNothing(t *testing.T) {
	resetRPCAlertState()
	dispatched := make(chan rpcJob, 4)
	setRPCDispatchForTest(t, func(job rpcJob) { dispatched <- job })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs := NewRPCObserver(ctx, nil, "chain-a")

	obs(rpcpool.EventRecovered, "http://a", nil)

	select {
	case job := <-dispatched:
		t.Fatalf("an unannounced recovery must not dispatch a RESOLVED, got %+v", job)
	case <-time.After(200 * time.Millisecond):
	}
}
