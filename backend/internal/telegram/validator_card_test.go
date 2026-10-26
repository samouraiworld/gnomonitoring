package telegram

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestMatchValidators(t *testing.T) {
	entries := []database.ValidatorReportEntry{
		{Addr: "g1beta", Moniker: "Alphabet"},
		{Addr: "g1alpha", Moniker: "alpha"},
		{Addr: "g1other", Moniker: "zeta"},
	}
	addrs := func(es []database.ValidatorReportEntry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Addr)
		}
		return out
	}

	assert.Equal(t, []string{"g1alpha"}, addrs(matchValidators(entries, "ALPHA")), "exact moniker wins over substrings")
	assert.Equal(t, []string{"g1beta"}, addrs(matchValidators(entries, " g1beta ")), "exact address")
	assert.Equal(t, []string{"g1alpha", "g1beta"}, addrs(matchValidators(entries, "lph")), "substrings, sorted by moniker")
	assert.Empty(t, matchValidators(entries, "nobody"))
}

// seedCardChain writes a short last-24h history for addr on chainID.
func seedCardChain(t *testing.T, db *gorm.DB, chainID, addr, moniker string, vp int64, lagMs int64) {
	t.Helper()
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		lag := lagMs + int64(i)
		late := false
		require.NoError(t, db.Create(&database.DailyParticipation{
			ChainID: chainID, Addr: addr, Moniker: moniker,
			BlockHeight: int64(i + 1), Date: now.Add(-time.Duration(10-i) * time.Minute),
			Participated: true, PrecommitLagMs: &lag, LateForQuorum: &late,
		}).Error)
	}
	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: chainID, Addr: addr, Moniker: moniker, VotingPower: vp}).Error)
}

func TestFormatValidatorCard_OneMatchShowsCard(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chainID := "card-one"
	seedCardChain(t, db, chainID, "g1evil", "<b>evil</b>", 30, 100)
	seedCardChain(t, db, chainID, "g1calm", "calm", 70, 10)
	alertAt := time.Now().UTC().Add(-26 * time.Hour)
	require.NoError(t, db.Create(&database.AlertLog{
		ChainID: chainID, Addr: "g1evil", Moniker: "<b>evil</b>", Level: "WARNING",
		StartHeight: 1, EndHeight: 5, SentAt: alertAt,
	}).Error)

	msg, err := formatValidatorCard(db, chainID, "<b>evil</b>", time.Now())
	require.NoError(t, err)

	assert.Contains(t, msg, "&lt;b&gt;evil&lt;/b&gt;", "moniker is HTML-escaped")
	assert.NotContains(t, msg, "<b>evil</b>")
	assert.Contains(t, msg, "<code>g1evil</code>")
	assert.Contains(t, msg, "Tier: <b>")
	assert.Contains(t, msg, "Score: <b>")
	assert.Contains(t, msg, "Sign rate: 100.00%")
	assert.Contains(t, msg, "Proposer reliability:")
	assert.Contains(t, msg, "Voting power: 30 (30.0%)")
	assert.Contains(t, msg, "Precommit lag 24h: p50 10")
	assert.Contains(t, msg, "Precommit lag 7d: p50 10")
	assert.Contains(t, msg, "Last alert: "+alertAt.Format("2006-01-02 15:04")+" UTC (1d 2h")
	assert.NotContains(t, msg, "calm")
}

func TestFormatValidatorCard_NoAlertNoLatency(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chainID := "card-quiet"
	require.NoError(t, db.Create(&database.DailyParticipation{
		ChainID: chainID, Addr: "g1quiet", Moniker: "quiet",
		BlockHeight: 1, Date: time.Now().UTC(), Participated: true,
	}).Error)
	require.NoError(t, db.Create(&database.AddrMoniker{ChainID: chainID, Addr: "g1quiet", Moniker: "quiet", VotingPower: 1}).Error)

	msg, err := formatValidatorCard(db, chainID, "g1quiet", time.Now())
	require.NoError(t, err)
	assert.Contains(t, msg, "Precommit lag 24h: n/a")
	assert.Contains(t, msg, "Last alert: none")
}

func TestFormatValidatorCard_SeveralMatchesListsCandidates(t *testing.T) {
	db := testoutils.NewTestDB(t)
	chainID := "card-many"
	seedCardChain(t, db, chainID, "g1one", "node-<1>", 1, 10)
	seedCardChain(t, db, chainID, "g1two", "node-2", 1, 10)

	msg, err := formatValidatorCard(db, chainID, "node", time.Now())
	require.NoError(t, err)
	assert.Contains(t, msg, "2 validators on <code>card-many</code> match <code>node</code>")
	assert.Contains(t, msg, "node-&lt;1&gt;")
	assert.Contains(t, msg, "<code>g1two</code>")
	assert.NotContains(t, msg, "Tier:")
}

func TestFormatValidatorCard_NoMatchIsScopedToChain(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedCardChain(t, db, "card-a", "g1here", "here", 1, 10)
	seedCardChain(t, db, "card-b", "g1there", "there", 1, 10)

	msg, err := formatValidatorCard(db, "card-a", "there", time.Now())
	require.NoError(t, err)
	assert.Contains(t, msg, "No validator of the active set on <code>card-a</code> matches <code>there</code>")

	msg, err = formatValidatorCard(db, "card-a", "<x>", time.Now())
	require.NoError(t, err)
	assert.Contains(t, msg, "<code>&lt;x&gt;</code>", "the query is escaped too")
}

func TestFormatValidatorCard_EmptyQueryShowsUsage(t *testing.T) {
	msg, err := formatValidatorCard(nil, "any", "  ", time.Now())
	require.NoError(t, err)
	assert.Equal(t, validatorCardUsage(), msg)
}

func TestValidatorCommandUsesActiveChain(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := fakeTelegram(t)
	seedCardChain(t, db, "card-active", "g1act", "activeval", 1, 10)
	setActiveChain(555, "card-active")
	t.Cleanup(func() { setActiveChain(555, "") })

	handlers := BuildTelegramHandlers("tok", db, "card-default", []string{"card-default", "card-active"})
	handlers["/validator"](555, "activeval")

	got := sent()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "activeval")
	assert.Contains(t, got[0], "[card-active]")
}

func TestHandleSearchInput_CardStateSendsCard(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := fakeTelegram(t)
	seedCardChain(t, db, "card-search", "g1srch", "searched", 1, 10)
	setSearchState(556, SearchState{
		BotType: "validator", ChainID: "card-search", Cmd: validatorCardSearchCmd,
		ExpiresAt: time.Now().Add(time.Minute),
	})

	require.True(t, HandleSearchInput("tok", db, "validator", 556, "searched"))
	got := sent()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "🪪 <b>searched</b>")
}

func TestHelpListsValidatorCommand(t *testing.T) {
	assert.Contains(t, formatHelp(), "/validator &lt;moniker or address&gt;")
	markup := buildActionMarkup("validators", nil)
	var found bool
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == encodeCmdCallback("a", "card") {
				found = true
			}
		}
	}
	assert.True(t, found, "/cmd validators menu offers the card")
}
