package telegram

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

// TestFormatStatusProposal_EmptyResult_ProducesWellFormedHTML verifies that
// when a chain has zero GovDAO proposals, formatStatusProposal returns a
// message with the <b> tag closed. Telegram's HTML parse mode rejects
// messages with unclosed tags, so an unclosed <b> here causes
// SendMessageTelegram to fail silently for any chain with no proposals.
func TestFormatStatusProposal_EmptyResult_ProducesWellFormedHTML(t *testing.T) {
	db := testoutils.NewTestDB(t)

	msg, err := formatStatusProposal(db, "chain-with-no-proposals", 10)
	require.NoError(t, err)
	assert.Contains(t, msg, "<b>", "message should open a <b> tag")
	assert.Contains(t, msg, "</b>", "message should close the <b> tag it opens")
}

// TestFormatGetLastExecute_EmptyResult_ProducesWellFormedHTML mirrors the
// above check for the /executedproposals empty-case message.
func TestFormatGetLastExecute_EmptyResult_ProducesWellFormedHTML(t *testing.T) {
	db := testoutils.NewTestDB(t)

	msg, err := FormatGetLastExecute(db, "chain-with-no-proposals", 10)
	require.NoError(t, err)
	assert.Contains(t, msg, "<b>", "message should open a <b> tag")
	assert.Contains(t, msg, "</b>", "message should close the <b> tag it opens")
}

// TestFormatStatusProposal_EscapesTitleAndUrl verifies that proposal Title
// and Url containing HTML-significant characters are escaped before being
// embedded in the HTML message, so a malicious or accidental "<" or "&" in a
// proposal title cannot break Telegram's HTML parsing.
func TestFormatStatusProposal_EscapesTitleAndUrl(t *testing.T) {
	db := testoutils.NewTestDB(t)

	chainID := "chain-with-unsafe-title"
	proposal := database.Govdao{
		Id:      1,
		ChainID: chainID,
		Url:     "https://example.com?a=1&b=2",
		Title:   "<script>alert(1)</script> & friends",
		Tx:      "TX1",
		Status:  "ACTIVE",
	}
	require.NoError(t, db.Create(&proposal).Error)

	msg, err := formatStatusProposal(db, chainID, 10)
	require.NoError(t, err)

	assert.NotContains(t, msg, "<script>", "raw title markup must not appear unescaped")
	assert.Contains(t, msg, "&lt;script&gt;", "title must be HTML-escaped")
	assert.Contains(t, msg, "https://example.com?a=1&amp;b=2", "url must be HTML-escaped")
}
