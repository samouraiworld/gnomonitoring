package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

// fakeTelegram points the package at an httptest server that accepts every
// sendMessage call and records its text. It returns a function that yields
// the texts sent so far.
func fakeTelegram(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		texts = append(texts, body.Text)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	restore := telegramAPIBaseURL
	telegramAPIBaseURL = srv.URL
	t.Cleanup(func() {
		telegramAPIBaseURL = restore
		srv.Close()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), texts...)
	}
}

// -------------------------------------------------------------------------
// GovDAO broadcasts follow each chat's active chain
// -------------------------------------------------------------------------

func TestGovdaoChatIDsForChain_FollowsActiveChain(t *testing.T) {
	enabled := []string{"gnoland1", "test12"}
	prefs := map[int64]string{
		1: "gnoland1",
		2: "test12",
		3: "betanet", // legacy migration default, not enabled: counts as default
		4: "gnoland1",
	}

	assert.Equal(t, []int64{1, 3, 4}, govdaoChatIDsForChain(prefs, "gnoland1", "gnoland1", enabled))
	assert.Equal(t, []int64{2}, govdaoChatIDsForChain(prefs, "test12", "gnoland1", enabled))
	assert.Empty(t, govdaoChatIDsForChain(prefs, "betanet", "gnoland1", enabled),
		"a disabled chain's notices reach no chat")
}

func TestMsgTelegramGovdao_SendsOnlyToChatsOnThatChain(t *testing.T) {
	db := testoutils.NewTestDB(t)
	sent := fakeTelegram(t)

	for chatID, chain := range map[int64]string{101: "gnoland1", 102: "test12"} {
		_, err := database.InsertChatID(db, chatID, "govdao", chain)
		require.NoError(t, err)
	}

	require.NoError(t, MsgTelegramGovdao("test12 notice", "test12", "token", "gnoland1", []string{"gnoland1", "test12"}, db))
	assert.Equal(t, []string{"test12 notice"}, sent())
}

// -------------------------------------------------------------------------
// /subscribe replies
// -------------------------------------------------------------------------

func TestFormatSubscriptionList_EscapesMonikers(t *testing.T) {
	msg := formatSubscriptionList("test12", []database.ValidatorStatus{
		{Moniker: "Tom & <Jerry>", Addr: "g1abc", Status: "on"},
	})
	assert.Contains(t, msg, "Tom &amp; &lt;Jerry&gt;")
	assert.NotContains(t, msg, "<Jerry>")
}

func TestFormatSubscriptionList_LongListSplitsUnderLimit(t *testing.T) {
	subs := make([]database.ValidatorStatus, 200)
	for i := range subs {
		subs[i] = database.ValidatorStatus{
			Moniker: strings.Repeat("m", 30),
			Addr:    "g1" + strings.Repeat("a", 38),
			Status:  "on",
		}
	}
	msg := formatSubscriptionList("test12", subs)
	require.Greater(t, len(msg), 4096, "fixture must exceed one Telegram message")

	chunks := splitTelegramChunks(msg)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c), 4096)
	}
	assert.Equal(t, msg, strings.Join(chunks, "\n"), "chunks must rebuild the full reply")
}

func TestFormatSubscriptionChange_OneSummaryEscaped(t *testing.T) {
	msg := formatSubscriptionChange("✅ Enabled", []string{"alpha", "b<eta>"}, 1)
	assert.Contains(t, msg, "<b>2</b> validator(s)")
	assert.Contains(t, msg, "• alpha")
	assert.Contains(t, msg, "• b&lt;eta&gt;")
	assert.Contains(t, msg, "Failed: <b>1</b>")
}

// -------------------------------------------------------------------------
// Pagination callbacks carry the chain
// -------------------------------------------------------------------------

func TestEncodeCallbackData_RoundTripsChain(t *testing.T) {
	data := encodeCallbackData("status", "test12", 3, 10, "current_week", "", "desc", "")
	cmdKey, chainID, page, _, _, _, _, _, ok := parseCallbackData(data)
	require.True(t, ok)
	assert.Equal(t, "status", cmdKey)
	assert.Equal(t, "test12", chainID)
	assert.Equal(t, 3, page)
}

func TestEncodeCallbackData_LegacyDataHasNoChain(t *testing.T) {
	_, chainID, _, _, _, _, _, _, ok := parseCallbackData("c=st&p=2&l=10")
	require.True(t, ok)
	assert.Empty(t, chainID, "old buttons fall back to the active chain")
}

func TestEncodeCallbackData_UnsafeChainOmitted(t *testing.T) {
	for _, chain := range []string{"a&n=b", "x=y", strings.Repeat("c", callbackChainMaxLen+1)} {
		data := encodeCallbackData("status", chain, 1, 10, "", "", "", "")
		assert.NotContains(t, data, "&n=", "chain %q must not be carried", chain)
	}
}

func TestEncodeCallbackData_FitsTelegramLimit(t *testing.T) {
	data := encodeCallbackData("tx_contrib", strings.Repeat("c", callbackChainMaxLen),
		9999, 100, "current_month", strings.Repeat("é", 40), "desc", "search")
	assert.LessOrEqual(t, len(data), callbackDataMaxLen)
	assert.Contains(t, data, "&n=")
}

// -------------------------------------------------------------------------
// GovDAO bot commands
// -------------------------------------------------------------------------

func TestParseGovdaoLimit_Bounds(t *testing.T) {
	assert.Equal(t, govdaoLimitDefault, parseGovdaoLimit(""))
	assert.Equal(t, govdaoLimitDefault, parseGovdaoLimit("abc"))
	assert.Equal(t, 1, parseGovdaoLimit("-5"))
	assert.Equal(t, 7, parseGovdaoLimit("7"))
	assert.Equal(t, govdaoLimitMax, parseGovdaoLimit("1000000000"))
}

func TestGovdaoStatus_DBErrorSendsMessage(t *testing.T) {
	db := testoutils.NewTestDB(t)
	require.NoError(t, db.Migrator().DropTable(&database.Govdao{}))
	sent := fakeTelegram(t)

	handlers := BuildTelegramGovdaoHandlers("token", db, "test12", []string{"test12"})
	handlers["/status"](1, "")

	texts := sent()
	require.Len(t, texts, 1)
	assert.NotEmpty(t, strings.TrimSpace(texts[0]))
	assert.Contains(t, texts[0], "Unable to fetch")
}

func TestGovdaoListHeadings(t *testing.T) {
	db := testoutils.NewTestDB(t)
	require.NoError(t, database.InsertGovdao(db, 1, "test12", "https://gno.land/r/gov/dao:1", "One", "tx1", "ACCEPTED", true))

	status, err := formatStatusProposal(db, "test12", 10)
	require.NoError(t, err)
	assert.Contains(t, status, "<b>GovDAO Proposals</b>\n\n🗳️ <b>Proposal Nº 1</b>",
		"the heading must end with a newline before the first proposal")

	executed, err := FormatGetLastExecute(db, "test12", 10)
	require.NoError(t, err)
	assert.NotContains(t, executed, "New Proposal")
	assert.Contains(t, executed, "<b>Last Executed Proposals</b>\n\n🗳️ [test12] <b>Executed Proposal Nº 1</b>")

	last, err := FormatGetLastProposal(db, "test12")
	require.NoError(t, err)
	assert.NotContains(t, last, "New Proposal")
	assert.Contains(t, last, "<b>Most Recent Proposal</b>\n\n")
}

// -------------------------------------------------------------------------
// /start and commands addressed to another bot
// -------------------------------------------------------------------------

func TestBothBotsAnswerStart(t *testing.T) {
	db := testoutils.NewTestDB(t)
	assert.Contains(t, BuildTelegramHandlers("token", db, "test12", []string{"test12"}), "/start")
	assert.Contains(t, BuildTelegramGovdaoHandlers("token", db, "test12", []string{"test12"}), "/start")
}

func commandMsg(text string) *message {
	cmdLen := len(strings.Fields(text)[0])
	return &message{Text: text, Entities: []tgEntity{{Type: "bot_command", Offset: 0, Length: cmdLen}}}
}

func TestExtractCommand_BotAddressing(t *testing.T) {
	cmd, args, ok := extractCommand(commandMsg("/status@MyBot limit=5"), "mybot")
	require.True(t, ok, "a command addressed to this bot is handled (case-insensitive)")
	assert.Equal(t, "/status", cmd)
	assert.Equal(t, "limit=5", args)

	_, _, ok = extractCommand(commandMsg("/status@OtherBot"), "mybot")
	assert.False(t, ok, "a command addressed to another bot is ignored")

	cmd, _, ok = extractCommand(commandMsg("/status"), "mybot")
	require.True(t, ok, "an unaddressed command is handled")
	assert.Equal(t, "/status", cmd)

	_, _, ok = extractCommand(commandMsg("/status@OtherBot"), "")
	assert.True(t, ok, "without a known username every command is accepted")
}
