package govdao

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tm2errors "github.com/gnolang/gno/tm2/pkg/errors"
	"github.com/samouraiworld/gnomonitoring/backend/internal/chainquery"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// notification records one dispatched status-change notification, so tests can
// assert on what would have been sent without touching Discord/Slack/Telegram.
type notification struct {
	chainID string
	id      int
	status  string
}

// stubStatusWatcher swaps the two package seams CheckProposalStatus goes
// through — the per-chain status fetch and the notification dispatch — and
// returns a pointer to the slice recording every notification sent. Seams are
// restored when the test ends.
//
// statuses maps a chain ID to the status its chain currently reports for any
// proposal. A chain absent from the map returns an error, standing in for an
// unreachable RPC pool.
func stubStatusWatcher(t *testing.T, statuses map[string]string) *[]notification {
	t.Helper()

	origFetch, origNotify, origChains := fetchChainProposalStatus, notifyProposalStatus, pollableChains
	t.Cleanup(func() {
		fetchChainProposalStatus, notifyProposalStatus, pollableChains = origFetch, origNotify, origChains
	})

	// Every chain these tests store proposals on is enabled unless a test
	// narrows it with stubEnabledChains.
	stubEnabledChains("dev", "betanet")

	fetchChainProposalStatus = func(chainID string, _ int) (string, error) {
		status, ok := statuses[chainID]
		if !ok {
			return "", fmt.Errorf("no RPC pool registered for chain %q", chainID)
		}
		return status, nil
	}

	// CheckProposalStatus polls chains concurrently, so notifications may
	// arrive from several goroutines. It returns only once every chain is
	// done, so tests read the slice afterwards without the lock.
	var (
		mu   sync.Mutex
		sent []notification
	)
	notifyProposalStatus = func(_ *gorm.DB, p database.Govdao, status string) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, notification{chainID: p.ChainID, id: p.Id, status: status})
	}

	return &sent
}

// countFetches wraps the current fetchChainProposalStatus seam and returns a
// function reporting how many times each chain was polled. Safe for the
// concurrent per-chain polling CheckProposalStatus does.
func countFetches() func(chainID string) int {
	var (
		mu    sync.Mutex
		calls = map[string]int{}
	)
	inner := fetchChainProposalStatus
	fetchChainProposalStatus = func(chainID string, id int) (string, error) {
		mu.Lock()
		calls[chainID]++
		mu.Unlock()
		return inner(chainID, id)
	}
	return func(chainID string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[chainID]
	}
}

// allEndpointsFailedErr is what a status fetch returns in production when
// every RPC endpoint of the chain failed: rpcpool's sentinel, wrapped by
// chainquery.Query with tm2's errors.Wrap.
func allEndpointsFailedErr() error {
	return tm2errors.Wrap(
		fmt.Errorf("%w: 2 tried, first error: %w", rpcpool.ErrAllEndpointsFailed, context.DeadlineExceeded),
		"query error")
}

// waitFor reports whether ch was closed within d.
func waitFor(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// stubEnabledChains sets the chains CheckProposalStatus polls. Call it after
// stubStatusWatcher, which restores the seam when the test ends.
func stubEnabledChains(chains ...string) {
	pollableChains = func() []string { return chains }
}

// insertProposal writes one govdaos row with full control over status and
// status_synced, which InsertGovdao does not expose.
func insertProposal(t *testing.T, db *gorm.DB, id int, chainID, status string, synced bool) {
	t.Helper()
	require.NoError(t, db.Create(&database.Govdao{
		Id:           id,
		ChainID:      chainID,
		Url:          fmt.Sprintf("https://gno.land/r/gov/dao:%d", id),
		Title:        fmt.Sprintf("Proposal #%d", id),
		Status:       status,
		StatusSynced: synced,
	}).Error)
}

func loadProposal(t *testing.T, db *gorm.DB, id int, chainID string) database.Govdao {
	t.Helper()
	var p database.Govdao
	require.NoError(t, db.Where("id = ? AND chain_id = ?", id, chainID).First(&p).Error)
	return p
}

// A proposal whose stored status was never confirmed against the chain by the
// current parser (status_synced = false) is reconciled silently. This is the
// deploy path: the pre-fix parser reported every rejected proposal as
// IN PROGRESS, so without this the first run would fire a burst of rejection
// notifications for the whole historical backlog.
func TestCheckProposalStatusReconcilesUnsyncedProposalSilently(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusRejected})

	insertProposal(t, db, 0, "dev", StatusInProgress, false)

	CheckProposalStatus(db)

	assert.Empty(t, *sent, "reconciling a never-confirmed status must not notify")
	p := loadProposal(t, db, 0, "dev")
	assert.Equal(t, StatusRejected, p.Status, "the stored status must be corrected")
	assert.True(t, p.StatusSynced, "the proposal must be marked as confirmed against the chain")
}

// Once a proposal's status has been confirmed against the chain, a later
// transition to REJECTED is a live event and must be notified. This is the
// behaviour issue #112 reported as missing entirely.
func TestCheckProposalStatusNotifiesLiveRejection(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusRejected})

	insertProposal(t, db, 7, "dev", StatusInProgress, true)

	CheckProposalStatus(db)

	require.Len(t, *sent, 1, "a live rejection must be notified exactly once")
	assert.Equal(t, notification{chainID: "dev", id: 7, status: StatusRejected}, (*sent)[0])
	assert.Equal(t, StatusRejected, loadProposal(t, db, 7, "dev").Status)
}

// Regression guard: the acceptance path predates this change and must keep
// working through the shared terminal-status handler.
func TestCheckProposalStatusNotifiesLiveAcceptance(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusAccepted})

	insertProposal(t, db, 7, "dev", StatusInProgress, true)

	CheckProposalStatus(db)

	require.Len(t, *sent, 1)
	assert.Equal(t, notification{chainID: "dev", id: 7, status: StatusAccepted}, (*sent)[0])
	assert.Equal(t, StatusAccepted, loadProposal(t, db, 7, "dev").Status)
}

// The watcher polls every 5 minutes and re-reads every proposal, including
// terminal ones. A proposal already stored as rejected must not re-notify.
func TestCheckProposalStatusDoesNotRenotifyTerminalProposal(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusRejected})

	insertProposal(t, db, 7, "dev", StatusRejected, true)

	CheckProposalStatus(db)
	CheckProposalStatus(db)

	assert.Empty(t, *sent, "an already-recorded rejection must not be re-announced")
}

// UNKNOWN means the render could not be read, not that anything happened to
// the proposal. It must neither notify nor overwrite the stored status, and
// must leave status_synced alone so a later readable render still reconciles.
func TestCheckProposalStatusIgnoresUnreadableRender(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusUnknown})

	insertProposal(t, db, 7, "dev", StatusInProgress, false)

	CheckProposalStatus(db)

	assert.Empty(t, *sent, "an unreadable render must never produce an alert")
	p := loadProposal(t, db, 7, "dev")
	assert.Equal(t, StatusInProgress, p.Status, "an unreadable render must not overwrite the stored status")
	assert.False(t, p.StatusSynced, "an unreadable render confirms nothing")
}

// A failing status fetch (no RPC pool, chain down) must be as inert as an
// unreadable render.
func TestCheckProposalStatusIgnoresFetchError(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{})

	insertProposal(t, db, 7, "dev", StatusInProgress, true)

	CheckProposalStatus(db)

	assert.Empty(t, *sent)
	assert.Equal(t, StatusInProgress, loadProposal(t, db, 7, "dev").Status)
}

// govdaos is keyed on (id, chain_id), so proposal #0 exists independently on
// every chain. Updating one must not touch its namesakes: the pre-fix update
// filtered on id alone and rewrote the status of proposal #0 on every chain.
func TestCheckProposalStatusUpdatesOnlyTheProposalsOwnChain(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{
		"dev":     StatusRejected,
		"betanet": StatusInProgress,
	})

	insertProposal(t, db, 0, "dev", StatusInProgress, true)
	insertProposal(t, db, 0, "betanet", StatusInProgress, true)

	CheckProposalStatus(db)

	assert.Equal(t, StatusRejected, loadProposal(t, db, 0, "dev").Status)
	assert.Equal(t, StatusInProgress, loadProposal(t, db, 0, "betanet").Status,
		"proposal #0 on betanet is a different proposal and must be untouched")
	require.Len(t, *sent, 1, "only the rejected chain's proposal may notify")
	assert.Equal(t, "dev", (*sent)[0].chainID)
}

// A proposal inserted with a status successfully read from the chain is
// already confirmed: a later transition is a live event, not a reconciliation.
func TestProcessProposalMarksSuccessfullyReadStatusAsSynced(t *testing.T) {
	db := testoutils.NewTestDB(t)

	origTitle, origStatus, origTx := fetchProposalTitle, fetchProposalStatus, fetchTxByHeight
	t.Cleanup(func() {
		fetchProposalTitle, fetchProposalStatus, fetchTxByHeight = origTitle, origStatus, origTx
	})
	fetchProposalTitle = func(int, *chainquery.Client) (string, error) { return "Onboard validator4", nil }
	fetchProposalStatus = func(int, *chainquery.Client) (string, error) { return StatusInProgress, nil }
	fetchTxByHeight = func(int, []string) (*TxBlock, error) { return nil, fmt.Errorf("no tx") }

	ProcessProposal(proposalCreatedTx(100, "5"), "Fetch", db, "dev", []string{"http://gql"}, &chainquery.Client{}, "https://gno.land")

	p := loadProposal(t, db, 5, "dev")
	assert.Equal(t, StatusInProgress, p.Status)
	assert.True(t, p.StatusSynced, "a status read from the chain is confirmed")
}

// An unreadable render yields UNKNOWN with no error. That is not a confirmed
// status: the row must stay open to silent reconciliation.
func TestProcessProposalDoesNotMarkUnknownStatusAsSynced(t *testing.T) {
	db := testoutils.NewTestDB(t)

	origTitle, origStatus, origTx := fetchProposalTitle, fetchProposalStatus, fetchTxByHeight
	t.Cleanup(func() {
		fetchProposalTitle, fetchProposalStatus, fetchTxByHeight = origTitle, origStatus, origTx
	})
	fetchProposalTitle = func(int, *chainquery.Client) (string, error) { return "Onboard validator4", nil }
	fetchProposalStatus = func(int, *chainquery.Client) (string, error) { return StatusUnknown, nil }
	fetchTxByHeight = func(int, []string) (*TxBlock, error) { return nil, fmt.Errorf("no tx") }

	ProcessProposal(proposalCreatedTx(100, "5"), "Fetch", db, "dev", []string{"http://gql"}, &chainquery.Client{}, "https://gno.land")

	p := loadProposal(t, db, 5, "dev")
	assert.Equal(t, StatusUnknown, p.Status)
	assert.False(t, p.StatusSynced, "an unreadable render confirms nothing")
}

// proposalCreatedTx builds the minimal websocket transaction carrying one
// ProposalCreated event for the given proposal ID.
func proposalCreatedTx(height int, id string) Transaction {
	return Transaction{
		BlockHeight: height,
		Response: Response{
			Events: []GnoEvent{{
				Type:  "ProposalCreated",
				Attrs: []Attr{{Key: "id", Value: id}},
			}},
		},
	}
}

// Only enabled chains have an RPC pool. A proposal stored for a disabled chain
// must not be polled at all (each poll used to log "no RPC pool registered"
// every cycle), and its row must stay untouched.
func TestCheckProposalStatusSkipsDisabledChains(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusRejected})
	stubEnabledChains("dev")

	var polled []string
	fetch := fetchChainProposalStatus
	fetchChainProposalStatus = func(chainID string, id int) (string, error) {
		polled = append(polled, chainID)
		return fetch(chainID, id)
	}

	insertProposal(t, db, 7, "dev", StatusInProgress, true)
	insertProposal(t, db, 7, "gnoland1", StatusInProgress, false)

	CheckProposalStatus(db)

	assert.Equal(t, []string{"dev"}, polled, "a disabled chain's proposals must not be polled")
	disabled := loadProposal(t, db, 7, "gnoland1")
	assert.Equal(t, StatusInProgress, disabled.Status)
	assert.False(t, disabled.StatusSynced, "a disabled chain's row must stay untouched")
	require.Len(t, *sent, 1)
	assert.Equal(t, "dev", (*sent)[0].chainID)
}

// With no enabled chain there is nothing to poll.
func TestCheckProposalStatusWithNoEnabledChainPollsNothing(t *testing.T) {
	db := testoutils.NewTestDB(t)
	stubStatusWatcher(t, map[string]string{"dev": StatusRejected})
	stubEnabledChains()
	fetchChainProposalStatus = func(chainID string, _ int) (string, error) {
		t.Fatalf("unexpected poll of chain %q", chainID)
		return "", nil
	}

	insertProposal(t, db, 7, "dev", StatusInProgress, true)

	CheckProposalStatus(db)

	assert.Equal(t, StatusInProgress, loadProposal(t, db, 7, "dev").Status)
}

// A chain whose every RPC endpoint failed is skipped for the rest of the
// cycle after chainUnreachableAfter failed fetches in a row. Without this, a
// chain whose endpoints hang costs the 60s RPC timeout per proposal
// (42 proposals = 42 min), which delayed GovDAO notifications for every chain
// by about an hour in prod.
func TestCheckProposalStatusSkipsChainAfterAllEndpointsFailed(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusAccepted})
	inner := fetchChainProposalStatus
	fetchChainProposalStatus = func(chainID string, id int) (string, error) {
		if chainID == "betanet" {
			return "", allEndpointsFailedErr()
		}
		return inner(chainID, id)
	}
	calls := countFetches()

	for id := range 5 {
		insertProposal(t, db, id, "betanet", StatusInProgress, true)
	}
	insertProposal(t, db, 7, "dev", StatusInProgress, true)

	CheckProposalStatus(db)

	assert.Equal(t, chainUnreachableAfter, calls("betanet"),
		"an unreachable chain must be given up on after a few failed fetches, not polled once per proposal")
	assert.Equal(t, 1, calls("dev"))
	require.Len(t, *sent, 1, "the reachable chain must still be checked and notified")
	assert.Equal(t, notification{chainID: "dev", id: 7, status: StatusAccepted}, (*sent)[0])
	for id := range 5 {
		p := loadProposal(t, db, id, "betanet")
		assert.Equal(t, StatusInProgress, p.Status, "a skipped proposal must stay untouched")
		assert.True(t, p.StatusSynced)
	}
}

// Only an outage of every endpoint skips the chain. A chain-level error on one
// proposal (unknown realm, VM panic) says nothing about the others, so the
// rest of that chain's proposals must still be checked in the same cycle.
func TestCheckProposalStatusChainLevelErrorDoesNotSkipChain(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusAccepted})

	// Fail the first fetch, whichever proposal it is, so the test does not
	// depend on the order rows come back from the database.
	var failed atomic.Bool
	inner := fetchChainProposalStatus
	fetchChainProposalStatus = func(chainID string, id int) (string, error) {
		if failed.CompareAndSwap(false, true) {
			return "", tm2errors.Wrap(errors.New("VM panic: index out of range"), "query error")
		}
		return inner(chainID, id)
	}
	calls := countFetches()

	for id := 1; id <= 3; id++ {
		insertProposal(t, db, id, "dev", StatusInProgress, true)
	}

	CheckProposalStatus(db)

	assert.Equal(t, 3, calls("dev"), "a chain-level error must not skip the chain's other proposals")
	assert.Len(t, *sent, 2, "every proposal fetched without error must be notified")
}

// An all-endpoints failure that is not followed by another one is not an
// outage: a proxy can answer 5xx on every endpoint for one proposal only (a
// render slow enough to hit a gateway timeout). Giving up on the chain there
// would skip the same later proposals every cycle, so an isolated failure
// must not skip the chain, and a successful fetch resets the count.
func TestCheckProposalStatusIsolatedOutagesDoNotSkipChain(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusAccepted})

	// Fail the 1st and 3rd fetches, whichever proposals they are, so the test
	// does not depend on the order rows come back from the database.
	var n atomic.Int32
	inner := fetchChainProposalStatus
	fetchChainProposalStatus = func(chainID string, id int) (string, error) {
		if call := n.Add(1); call == 1 || call == 3 {
			return "", allEndpointsFailedErr()
		}
		return inner(chainID, id)
	}
	calls := countFetches()

	for id := 1; id <= 4; id++ {
		insertProposal(t, db, id, "dev", StatusInProgress, true)
	}

	CheckProposalStatus(db)

	assert.Equal(t, 4, calls("dev"), "failures separated by a success must not skip the chain")
	assert.Len(t, *sent, 2, "every proposal fetched without error must be notified")
}

// Two proposals whose render always fails on every endpoint, on a chain that
// answers for the others, trip the skip whenever they are fetched first and
// side by side. Checked in a fixed order (rows come back in insertion order
// here: 1, 2, 3), they would do so every cycle and proposal 3 would never be
// checked. Shuffled per cycle, proposal 3 is reached in any cycle where 1 and
// 2 are not both first (2 orders in 3), so 50 cycles leave a (1/3)^50 chance
// of a spurious failure.
func TestCheckProposalStatusFailingPairDoesNotStarveLaterProposals(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusAccepted})
	inner := fetchChainProposalStatus
	fetchChainProposalStatus = func(chainID string, id int) (string, error) {
		if id == 1 || id == 2 {
			return "", allEndpointsFailedErr()
		}
		return inner(chainID, id)
	}

	for id := 1; id <= 3; id++ {
		insertProposal(t, db, id, "dev", StatusInProgress, true)
	}

	for cycle := 0; cycle < 50 && len(*sent) == 0; cycle++ {
		CheckProposalStatus(db)
	}

	require.Len(t, *sent, 1, "proposal 3 must be checked within a few cycles")
	assert.Equal(t, notification{chainID: "dev", id: 3, status: StatusAccepted}, (*sent)[0])
}

// A chain that hangs only delays itself: another chain's ACCEPTED must go out
// in the same cycle while the slow chain is still blocked. Each fake waits on
// the other chain with a deadline, so the test fails (instead of hanging) when
// chains are polled one after the other, whatever their order:
//   - slow chain first: it waits for dev's notification, which never comes
//     until it gives up;
//   - dev first: dev waits for the slow chain to start, which never happens
//     until it gives up.
func TestCheckProposalStatusSlowChainDoesNotDelayOtherChains(t *testing.T) {
	const deadline = 5 * time.Second

	db := testoutils.NewTestDB(t)
	sent := stubStatusWatcher(t, map[string]string{"dev": StatusAccepted})

	slowStarted := make(chan struct{})
	devNotified := make(chan struct{})
	// Each chain holds a single proposal today; OnceFunc keeps the test valid
	// (no double close) if a row is ever added.
	markSlowStarted := sync.OnceFunc(func() { close(slowStarted) })
	markDevNotified := sync.OnceFunc(func() { close(devNotified) })
	var timedOut atomic.Bool

	inner := fetchChainProposalStatus
	fetchChainProposalStatus = func(chainID string, id int) (string, error) {
		switch chainID {
		case "betanet":
			markSlowStarted()
			if !waitFor(devNotified, deadline) {
				timedOut.Store(true)
			}
			return "", allEndpointsFailedErr()
		case "dev":
			if !waitFor(slowStarted, deadline) {
				timedOut.Store(true)
			}
		}
		return inner(chainID, id)
	}
	notify := notifyProposalStatus
	notifyProposalStatus = func(db *gorm.DB, p database.Govdao, status string) {
		notify(db, p, status)
		if p.ChainID == "dev" {
			markDevNotified()
		}
	}

	insertProposal(t, db, 0, "betanet", StatusInProgress, true)
	insertProposal(t, db, 7, "dev", StatusInProgress, true)

	CheckProposalStatus(db)

	assert.False(t, timedOut.Load(), "dev's notification must not wait for the slow chain")
	require.Len(t, *sent, 1)
	assert.Equal(t, notification{chainID: "dev", id: 7, status: StatusAccepted}, (*sent)[0])
	assert.Equal(t, StatusAccepted, loadProposal(t, db, 7, "dev").Status)
}

// The skip above keys on rpcpool.ErrAllEndpointsFailed. In production the
// error travels through chainquery.Query, which wraps it with tm2's
// errors.Wrap; this checks the sentinel is still recognizable on the real
// path, against a real pool whose only endpoint answers 503 (an endpoint
// failure, as a proxy in front of a dead node would answer).
func TestExtractProposalRenderKeepsAllEndpointsFailedRecognizable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	client := &chainquery.Client{RPCClient: rpcpool.New([]string{srv.URL})}
	_, err := ExtractProposalRender(1, client)

	require.ErrorIs(t, err, rpcpool.ErrAllEndpointsFailed)
}
