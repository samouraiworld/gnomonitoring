package govdao

import (
	"fmt"
	"testing"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
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

	origFetch, origNotify := fetchChainProposalStatus, notifyProposalStatus
	t.Cleanup(func() {
		fetchChainProposalStatus, notifyProposalStatus = origFetch, origNotify
	})

	fetchChainProposalStatus = func(chainID string, _ int) (string, error) {
		status, ok := statuses[chainID]
		if !ok {
			return "", fmt.Errorf("no RPC pool registered for chain %q", chainID)
		}
		return status, nil
	}

	var sent []notification
	notifyProposalStatus = func(_ *gorm.DB, p database.Govdao, status string) {
		sent = append(sent, notification{chainID: p.ChainID, id: p.Id, status: status})
	}

	return &sent
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
	fetchProposalTitle = func(int, *gnoclient.Client) (string, error) { return "Onboard validator4", nil }
	fetchProposalStatus = func(int, *gnoclient.Client) (string, error) { return StatusInProgress, nil }
	fetchTxByHeight = func(int, []string) (*TxBlock, error) { return nil, fmt.Errorf("no tx") }

	ProcessProposal(proposalCreatedTx(100, "5"), "Fetch", db, "dev", []string{"http://gql"}, &gnoclient.Client{}, "https://gno.land")

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
	fetchProposalTitle = func(int, *gnoclient.Client) (string, error) { return "Onboard validator4", nil }
	fetchProposalStatus = func(int, *gnoclient.Client) (string, error) { return StatusUnknown, nil }
	fetchTxByHeight = func(int, []string) (*TxBlock, error) { return nil, fmt.Errorf("no tx") }

	ProcessProposal(proposalCreatedTx(100, "5"), "Fetch", db, "dev", []string{"http://gql"}, &gnoclient.Client{}, "https://gno.land")

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
