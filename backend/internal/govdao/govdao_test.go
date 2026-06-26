package govdao

import (
	"fmt"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcessProposalSurvivesTitleError is a regression test for a production
// bug where a ProposalCreated event on test-13 was silently dropped: the
// `gno.land/r/gov/dao.proposals.GetProposal(id).Title()` qeval query returned a
// "nil pointer dereference" error because test-13's gov/dao realm differs from
// betanet's, and ProcessProposal used to `continue` on that error — never
// inserting nor announcing the proposal. Since the websocket delivers each
// event only once, the proposal was lost forever.
//
// Enrichment (title/status/tx) must now be best-effort: on failure the proposal
// is still inserted (and, for socket events, announced) with fallback values.
func TestProcessProposalSurvivesTitleError(t *testing.T) {
	db := testoutils.NewTestDB(t)

	origTitle, origStatus, origTx := fetchProposalTitle, fetchProposalStatus, fetchTxByHeight
	t.Cleanup(func() {
		fetchProposalTitle, fetchProposalStatus, fetchTxByHeight = origTitle, origStatus, origTx
	})
	fetchProposalTitle = func(int, string) (string, error) {
		return "", fmt.Errorf("runtime error: nil pointer dereference")
	}
	fetchProposalStatus = func(int, string) (string, error) {
		return "", fmt.Errorf("render unavailable")
	}
	fetchTxByHeight = func(int, []string) (*TxBlock, error) {
		return nil, fmt.Errorf("no tx")
	}

	tx := Transaction{
		BlockHeight: 100,
		Response: Response{
			Events: []GnoEvent{{
				Type:  "ProposalCreated",
				Attrs: []Attr{{Key: "id", Value: "23"}},
			}},
		},
	}

	// who="Fetch" exercises the insert path without dispatching real
	// Telegram/Discord notifications; the notify branch follows the same
	// non-aborting flow.
	ProcessProposal(tx, "Fetch", db, "test-13", []string{"http://gql"}, "http://rpc", "https://test13.testnets.gno.land")

	var p database.Govdao
	require.NoError(t, db.Where("chain_id = ? AND id = ?", "test-13", 23).First(&p).Error,
		"proposal must be stored even when title/status/tx enrichment fails")
	assert.Equal(t, "Proposal #23", p.Title)
	assert.Equal(t, "UNKNOWN", p.Status)
	assert.Equal(t, "https://test13.testnets.gno.land/r/gov/dao:23", p.Url)
	assert.Empty(t, p.Tx)
}
