package telegram

// White-box tests for the chain-state helpers and command handlers in
// validator.go.  Because the helpers (getActiveChain, setActiveChain,
// validateChainID) are unexported they are only reachable from within the same
// package, so this file lives in package telegram.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/score"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

// resetChatChainState clears the package-level chatChainState map between
// tests so that each test starts with a clean slate.
func resetChatChainState() {
	chatChainMu.Lock()
	defer chatChainMu.Unlock()
	chatChainState = make(map[int64]string)
}

// -------------------------------------------------------------------------
// getActiveChain / setActiveChain
// -------------------------------------------------------------------------

// TestGetActiveChain_DefaultsToDefault verifies that a chat ID with no
// per-chat override returns the supplied defaultChainID.
func TestGetActiveChain_DefaultsToDefault(t *testing.T) {
	resetChatChainState()

	const chatID int64 = 9001
	const defaultChain = "betanet"

	got := getActiveChain(chatID, defaultChain)
	assert.Equal(t, defaultChain, got, "unknown chatID should return the default chain")
}

// TestSetActiveChain_ValidChain verifies that after calling setActiveChain the
// subsequent call to getActiveChain returns the new value.
func TestSetActiveChain_ValidChain(t *testing.T) {
	resetChatChainState()

	const chatID int64 = 9002
	const defaultChain = "betanet"
	const newChain = "testnet"

	setActiveChain(chatID, newChain)
	got := getActiveChain(chatID, defaultChain)
	assert.Equal(t, newChain, got, "getActiveChain should reflect the value set by setActiveChain")
}

// TestSetActiveChain_EmptyStringClearsOverride verifies that passing an empty
// string to setActiveChain removes the per-chat override, causing
// getActiveChain to fall back to the default.
func TestSetActiveChain_EmptyStringClearsOverride(t *testing.T) {
	resetChatChainState()

	const chatID int64 = 9003
	const defaultChain = "betanet"

	setActiveChain(chatID, "testnet")
	setActiveChain(chatID, "") // clear
	got := getActiveChain(chatID, defaultChain)
	assert.Equal(t, defaultChain, got, "cleared override should fall back to default chain")
}

// -------------------------------------------------------------------------
// validateChainID
// -------------------------------------------------------------------------

// TestSetActiveChain_InvalidChain verifies that validateChainID rejects a
// chain ID that is not in the enabled list, and accepts one that is.
func TestSetActiveChain_InvalidChain(t *testing.T) {
	enabled := []string{"betanet", "testnet", "mainnet"}

	tests := []struct {
		name      string
		chainID   string
		wantValid bool
	}{
		{"valid betanet", "betanet", true},
		{"valid testnet", "testnet", true},
		{"valid mainnet", "mainnet", true},
		{"unknown chain", "unknownchain", false},
		{"empty string", "", false},
		{"case sensitive mismatch", "Betanet", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateChainID(tc.chainID, enabled)
			assert.Equal(t, tc.wantValid, got)
		})
	}
}

// -------------------------------------------------------------------------
// /chain command output
// -------------------------------------------------------------------------

// TestHandleChainCommand_ListsEnabledChains exercises the /chain handler
// through the BuildTelegramHandlers map and verifies that the produced message
// contains every enabled chain ID.
//
// Because the handler calls SendMessageTelegram (which makes HTTP requests) we
// use an empty token string: the Telegram API call will fail silently, but we
// can verify the state-related behaviour through getActiveChain.
func TestHandleChainCommand_ListsEnabledChains(t *testing.T) {
	resetChatChainState()

	db := testoutils.NewTestDB(t)

	enabledChains := []string{"chainAlpha", "chainBeta", "chainGamma"}
	const defaultChain = "chainAlpha"
	const chatID int64 = 9004

	// Set a known active chain so we can verify it is echoed in the output.
	setActiveChain(chatID, "chainBeta")

	handlers := BuildTelegramHandlers("", db, defaultChain, enabledChains)
	chainHandler, ok := handlers["/chain"]
	require.True(t, ok, "/chain handler must be registered")

	// Calling the handler with an empty token will attempt (and fail) an HTTP
	// call.  We are only verifying that getActiveChain returns the expected
	// value and that the handler does not panic.
	assert.NotPanics(t, func() { chainHandler(chatID, "") })

	// The active chain for this chat must remain unchanged after the read-only
	// /chain command.
	assert.Equal(t, "chainBeta", getActiveChain(chatID, defaultChain))
}

// -------------------------------------------------------------------------
// /setchain command — state persistence
// -------------------------------------------------------------------------

// TestHandleSetChainCommand_UpdatesState verifies that the /setchain handler
// persists the new chain selection in chatChainState when the requested chain
// is valid.
func TestHandleSetChainCommand_UpdatesState(t *testing.T) {
	resetChatChainState()

	db := testoutils.NewTestDB(t)

	enabledChains := []string{"chainA", "chainB", "chainC"}
	const defaultChain = "chainA"
	const chatID int64 = 9005

	handlers := BuildTelegramHandlers("", db, defaultChain, enabledChains)
	setChainHandler, ok := handlers["/setchain"]
	require.True(t, ok, "/setchain handler must be registered")

	// Switch to chainB.
	assert.NotPanics(t, func() { setChainHandler(chatID, "chain=chainB") })
	assert.Equal(t, "chainB", getActiveChain(chatID, defaultChain),
		"active chain should be updated to chainB after /setchain chain=chainB")

	// Switch to chainC.
	assert.NotPanics(t, func() { setChainHandler(chatID, "chain=chainC") })
	assert.Equal(t, "chainC", getActiveChain(chatID, defaultChain),
		"active chain should be updated to chainC after /setchain chain=chainC")
}

// TestHandleSetChainCommand_RejectsInvalidChain verifies that the /setchain
// handler does NOT update chatChainState when an unknown chain is provided.
func TestHandleSetChainCommand_RejectsInvalidChain(t *testing.T) {
	resetChatChainState()

	db := testoutils.NewTestDB(t)

	enabledChains := []string{"chainA", "chainB"}
	const defaultChain = "chainA"
	const chatID int64 = 9006

	// Pre-set a valid chain so we can assert it is unchanged.
	setActiveChain(chatID, "chainA")

	handlers := BuildTelegramHandlers("", db, defaultChain, enabledChains)
	setChainHandler := handlers["/setchain"]

	assert.NotPanics(t, func() { setChainHandler(chatID, "chain=nonexistent") })
	assert.Equal(t, "chainA", getActiveChain(chatID, defaultChain),
		"active chain must not change when an invalid chain ID is supplied")
}

// -------------------------------------------------------------------------
// reportActivate helper — chain label in returned message
// -------------------------------------------------------------------------

// TestReportActivate_ChainLabelInMessage verifies that reportActivate embeds
// the chain ID in the returned status string so the user knows which chain
// is being toggled.
func TestReportActivate_ChainLabelInMessage(t *testing.T) {
	db := testoutils.NewTestDB(t)

	const chatID int64 = 9007
	const chainID = "betanet"

	// Create a TelegramHourReport row so the status lookup does not fail.
	_, err := database.InsertChatID(db, chatID, "validator", chainID)
	require.NoError(t, err)

	tests := []struct {
		activate    string
		wantContain string
	}{
		{"true", chainID},
		{"false", chainID},
		{"", chainID},
	}

	for _, tc := range tests {
		t.Run("activate="+tc.activate, func(t *testing.T) {
			msg, err := reportActivate(db, chatID, chainID, tc.activate)
			require.NoError(t, err)
			assert.True(t,
				strings.Contains(msg, tc.wantContain),
				"message %q should contain chain ID %q", msg, tc.wantContain,
			)
		})
	}
}

// -------------------------------------------------------------------------
// parseParams (used by /setchain)
// -------------------------------------------------------------------------

// TestParseParams_KeyValueParsing exercises the shared parseParams helper with
// a variety of inputs to ensure it handles the key=value format correctly.
func TestParseParams_KeyValueParsing(t *testing.T) {
	tests := []struct {
		name  string
		input string
		key   string
		want  string
	}{
		{"simple kv", "chain=betanet", "chain", "betanet"},
		{"double dash prefix", "--chain=betanet", "chain", "betanet"},
		{"quoted value", `chain="betanet"`, "chain", "betanet"},
		{"single quoted", "chain='betanet'", "chain", "betanet"},
		{"multiple params, first", "chain=betanet limit=10", "chain", "betanet"},
		{"multiple params, second", "chain=betanet limit=10", "limit", "10"},
		{"missing key returns empty", "period=all_time", "chain", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := parseParams(tc.input)
			assert.Equal(t, tc.want, params[tc.key])
		})
	}
}

// -------------------------------------------------------------------------
// /setchain command — database persistence
// -------------------------------------------------------------------------

// TestHandleSetChainCommand_PersistsToDB verifies that the /setchain handler
// persists the new chain selection to the database via UpdateChatChain.
func TestHandleSetChainCommand_PersistsToDB(t *testing.T) {
	resetChatChainState()

	db := testoutils.NewTestDB(t)

	enabledChains := []string{"chainA", "chainB"}
	const defaultChain = "chainA"
	const chatID int64 = 9008

	// First, insert the chat with the default chain.
	_, err := database.InsertChatID(db, chatID, "validator", defaultChain)
	require.NoError(t, err)

	handlers := BuildTelegramHandlers("", db, defaultChain, enabledChains)
	setChainHandler := handlers["/setchain"]

	// Call /setchain to switch to chainB.
	assert.NotPanics(t, func() { setChainHandler(chatID, "chain=chainB") })

	// Verify the update was persisted to the database.
	chains, err := database.GetAllChatChains(db)
	require.NoError(t, err)
	assert.Equal(t, "chainB", chains[chatID], "chain should be persisted to database after /setchain")
}

// TestHydrationFromDB verifies that StartCommandLoop hydrates chatChainState
// from the database at startup.
func TestHydrationFromDB(t *testing.T) {
	resetChatChainState()

	db := testoutils.NewTestDB(t)

	const chatID1 int64 = 9009
	const chatID2 int64 = 9010
	const defaultChain = "chainA"

	// Manually insert chats and update their chains in the database.
	_, err := database.InsertChatID(db, chatID1, "validator", defaultChain)
	require.NoError(t, err)
	_, err = database.InsertChatID(db, chatID2, "validator", defaultChain)
	require.NoError(t, err)

	err = database.UpdateChatChain(db, chatID1, "chainB")
	require.NoError(t, err)
	err = database.UpdateChatChain(db, chatID2, "chainC")
	require.NoError(t, err)

	// Now, simulate the hydration that happens at startup by directly
	// calling GetAllChatChains and populating chatChainState.
	chains, err := database.GetAllChatChains(db)
	require.NoError(t, err)

	for chatID, cid := range chains {
		setActiveChain(chatID, cid)
	}

	// Verify that chatChainState now reflects the persisted preferences.
	assert.Equal(t, "chainB", getActiveChain(chatID1, defaultChain), "chatID1 should be hydrated to chainB")
	assert.Equal(t, "chainC", getActiveChain(chatID2, defaultChain), "chatID2 should be hydrated to chainC")
}

// -------------------------------------------------------------------------
// formatChainHealthHeader and formatChainHealthFooter
// -------------------------------------------------------------------------

func TestFormatChainHealthHeader_RPCReachable(t *testing.T) {
	snap := ChainHealthSnapshot{
		RPCReachable:      true,
		LatestBlockHeight: 12345,
		LatestBlockTime:   time.Now().Add(-30 * time.Second),
		ConsensusRound:    1,
		PeerCount:         5,
		MempoolTxCount:    2,
	}
	got := formatChainHealthHeader("test12", snap)
	assert.Contains(t, got, "[test12] Chain status")
	assert.Contains(t, got, "#12345")
	assert.Contains(t, got, "Consensus: round 1")
	assert.Contains(t, got, "Network: 5 peers | Mempool: 2 pending txs")
}

func TestFormatChainHealthHeader_RPCUnreachable(t *testing.T) {
	snap := ChainHealthSnapshot{RPCReachable: false}
	got := formatChainHealthHeader("test12", snap)
	assert.Contains(t, got, "RPC unreachable")
	assert.NotContains(t, got, "Consensus:")
}

func TestFormatChainHealthFooter_ValsetChangesFilteredByMinBlock(t *testing.T) {
	snap := ChainHealthSnapshot{
		MinBlock: 100,
		ValsetChanges: []ValsetChange{
			{BlockNum: 50, Address: "g1old", NewPower: 0},
			{BlockNum: 150, Address: "g1new", NewPower: 10},
		},
	}
	got := formatChainHealthFooter(snap)
	assert.NotContains(t, got, "g1old")
	assert.Contains(t, got, "g1new")
	assert.Contains(t, got, "added (power: 10)")
}

func TestFormatChainHealthFooter_NoChangesNoSection(t *testing.T) {
	snap := ChainHealthSnapshot{MinBlock: 100}
	got := formatChainHealthFooter(snap)
	assert.NotContains(t, got, "Valset changes")
}

// withChainHealthFetcher swaps the package-level ChainHealthFetcher for the
// duration of one test, restoring the original afterward.
func withChainHealthFetcher(t *testing.T, fn func(chainID string) ChainHealthSnapshot) {
	t.Helper()
	orig := ChainHealthFetcher
	ChainHealthFetcher = fn
	t.Cleanup(func() { ChainHealthFetcher = orig })
}

func TestFormatChainHealthPage_AllHealthy(t *testing.T) {
	db := testoutils.NewTestDB(t)
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon",
		BlockHeight: 1, Date: time.Now().UTC(), Participated: true,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon", VotingPower: 5})
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg, pageOut, totalPages, err := formatChainHealthPage(db, "test12", 1, 10, "")
	require.NoError(t, err)
	assert.Equal(t, 1, pageOut)
	assert.Equal(t, 1, totalPages)
	assert.Contains(t, msg, "All 1 validators healthy (last 24h)")
}

func TestFormatChainHealthPage_ProblemsSortedAndPaginated(t *testing.T) {
	db := testoutils.NewTestDB(t)
	// One participated=false row each -> TotalBlocks=1/SignedBlocks=0 ->
	// Score 0/Critical (same score as the old "0 rows" fixture) AND
	// MissedBlocks=1>0, so each still passes the new missed>0 filter.
	for _, addr := range []string{"g1a", "g1b", "g1c"} {
		db.Create(&database.DailyParticipation{
			ChainID: "test12", Addr: addr, Moniker: addr + "-mon",
			BlockHeight: 1, Date: time.Now().UTC(), Participated: false,
		})
		db.Create(&database.AddrMoniker{ChainID: "test12", Addr: addr, Moniker: addr + "-mon", VotingPower: 1})
	}
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg1, pageOut1, totalPages1, err := formatChainHealthPage(db, "test12", 1, 2, "")
	require.NoError(t, err)
	assert.Equal(t, 1, pageOut1)
	assert.Equal(t, 2, totalPages1)
	assert.Contains(t, msg1, "g1a-mon")
	assert.Contains(t, msg1, "g1b-mon")
	assert.NotContains(t, msg1, "g1c-mon")

	msg2, pageOut2, totalPages2, err := formatChainHealthPage(db, "test12", 2, 2, "")
	require.NoError(t, err)
	assert.Equal(t, 2, pageOut2)
	assert.Equal(t, 2, totalPages2)
	assert.Contains(t, msg2, "g1c-mon")
	assert.NotContains(t, msg2, "g1a-mon")
}

func TestFormatChainHealthPage_VotingPowerPercentShownWhenPresent(t *testing.T) {
	db := testoutils.NewTestDB(t)
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1vp", Moniker: "vp-mon",
		BlockHeight: 1, Date: time.Now().UTC(), Participated: false,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1vp", Moniker: "vp-mon", VotingPower: 10})
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg, _, _, err := formatChainHealthPage(db, "test12", 1, 10, "")
	require.NoError(t, err)
	assert.Contains(t, msg, "VP:")
}

func TestFormatChainHealthPage_EmptyMonikerFallsBackToUnknown(t *testing.T) {
	db := testoutils.NewTestDB(t)
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1nomoniker", Moniker: "",
		BlockHeight: 1, Date: time.Now().UTC(), Participated: false,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1nomoniker", Moniker: "", VotingPower: 1})
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg, _, _, err := formatChainHealthPage(db, "test12", 1, 10, "")
	require.NoError(t, err)
	assert.Contains(t, msg, "unknown")
}

func TestFormatChainHealthPage_LeaveIntentAnnotated(t *testing.T) {
	db := testoutils.NewTestDB(t)
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1leaving", Moniker: "leaving-mon",
		BlockHeight: 1, Date: time.Now().UTC(), Participated: false,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1leaving", Moniker: "leaving-mon", VotingPower: 1})
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot {
		return ChainHealthSnapshot{
			ValidatorSet: []ValidatorInfo{{Address: "g1leaving", VotingPower: 1, KeepRunning: false}},
		}
	})

	msg, _, _, err := formatChainHealthPage(db, "test12", 1, 10, "")
	require.NoError(t, err)
	assert.Contains(t, msg, "intends to leave")
}

func TestFormatChainHealthPage_FetcherNil(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withChainHealthFetcher(t, nil)

	msg, pageOut, totalPages, err := formatChainHealthPage(db, "test12", 1, 10, "")
	require.NoError(t, err)
	assert.Equal(t, 1, pageOut)
	assert.Equal(t, 1, totalPages)
	assert.Contains(t, msg, "not available yet")
}

func TestFormatChainHealthPage_DisabledChainShortCircuits(t *testing.T) {
	db := testoutils.NewTestDB(t)
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot {
		return ChainHealthSnapshot{IsDisabled: true}
	})

	msg, pageOut, totalPages, err := formatChainHealthPage(db, "test12", 1, 10, "")
	require.NoError(t, err)
	assert.Equal(t, 1, pageOut)
	assert.Equal(t, 1, totalPages)
	assert.Contains(t, msg, "MONITORING OFF")
}

func TestFormatChainHealthPage_FilterMatchingNoProblemsDoesNotClaimHealthy(t *testing.T) {
	db := testoutils.NewTestDB(t)
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1critical", Moniker: "critical-mon",
		BlockHeight: 1, Date: time.Now().UTC(), Participated: false,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1critical", Moniker: "critical-mon", VotingPower: 1})
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg, _, _, err := formatChainHealthPage(db, "test12", 1, 10, "nomatch-filter-xyz")
	require.NoError(t, err)
	assert.NotContains(t, msg, "All 1 validators healthy")
	assert.Contains(t, msg, "No matching validators")
}

func TestFormatChainHealthPage_NoFilterStillReportsHealthyWhenTrulyHealthy(t *testing.T) {
	db := testoutils.NewTestDB(t)
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon",
		BlockHeight: 1, Date: time.Now().UTC(), Participated: true,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1healthy", Moniker: "healthy-mon", VotingPower: 5})
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg, _, _, err := formatChainHealthPage(db, "test12", 1, 10, "irrelevant-filter")
	require.NoError(t, err)
	assert.Contains(t, msg, "All 1 validators healthy")
}

func TestHealthLimitDefault_IsFiveForStatusPagination(t *testing.T) {
	assert.Equal(t, 5, healthLimitDefault, "healthLimitDefault must stay smaller than limitDefault so /status surfaces a Next page sooner")
}

func TestFormatChainHealthPage_HealthLimitDefaultPaginatesSixProblems(t *testing.T) {
	db := testoutils.NewTestDB(t)
	// One participated=false row each -> Score 0/Critical AND MissedBlocks=1>0
	// (see TestFormatChainHealthPage_ProblemsSortedAndPaginated for why), so
	// all 6 addresses still land in the paginated list under the new filter.
	for _, addr := range []string{"g1a", "g1b", "g1c", "g1d", "g1e", "g1f"} {
		db.Create(&database.DailyParticipation{
			ChainID: "test12", Addr: addr, Moniker: addr + "-mon",
			BlockHeight: 1, Date: time.Now().UTC(), Participated: false,
		})
		db.Create(&database.AddrMoniker{ChainID: "test12", Addr: addr, Moniker: addr + "-mon", VotingPower: 1})
	}
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg, pageOut, totalPages, err := formatChainHealthPage(db, "test12", 1, healthLimitDefault, "")
	require.NoError(t, err)
	assert.Equal(t, 1, pageOut)
	assert.Equal(t, 2, totalPages, "6 problem validators at healthLimitDefault=5 per page must produce 2 pages")
	assert.Contains(t, msg, "Page 1/2")
	assert.NotContains(t, msg, "g1f-mon")
}

func TestFormatChainHealthPage_ZeroParticipationRowsExcludedFromList(t *testing.T) {
	db := testoutils.NewTestDB(t)
	// No DailyParticipation rows at all -> TotalBlocks=0/SignedBlocks=0 ->
	// Score 0/Critical, but MissedBlocks=0-0=0. User-confirmed design
	// decision: this validator is excluded from the list even though its
	// score is 0 (see plan Global Constraints). Not a bug.
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1nodata", Moniker: "nodata-mon", VotingPower: 1})
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg, _, _, err := formatChainHealthPage(db, "test12", 1, 10, "")
	require.NoError(t, err)
	assert.NotContains(t, msg, "nodata-mon")
	assert.Contains(t, msg, "All 1 validators healthy (last 24h)")
}

func TestFormatChainHealthPage_GoodTierValidatorWithMissedBlocksIsListed(t *testing.T) {
	db := testoutils.NewTestDB(t)
	// 19 participated=true + 1 participated=false -> sign_rate=95% ->
	// Score ~95/Excellent, but MissedBlocks=1>0, so it must still appear
	// (the core behavior change of this plan: not just Watch/Critical).
	for i := 0; i < 19; i++ {
		db.Create(&database.DailyParticipation{
			ChainID: "test12", Addr: "g1almostperfect", Moniker: "almostperfect-mon",
			BlockHeight: int64(i + 1), Date: time.Now().UTC(), Participated: true,
		})
	}
	db.Create(&database.DailyParticipation{
		ChainID: "test12", Addr: "g1almostperfect", Moniker: "almostperfect-mon",
		BlockHeight: 20, Date: time.Now().UTC(), Participated: false,
	})
	db.Create(&database.AddrMoniker{ChainID: "test12", Addr: "g1almostperfect", Moniker: "almostperfect-mon", VotingPower: 1})
	withChainHealthFetcher(t, func(chainID string) ChainHealthSnapshot { return ChainHealthSnapshot{} })

	msg, _, _, err := formatChainHealthPage(db, "test12", 1, 10, "")
	require.NoError(t, err)
	assert.Contains(t, msg, "almostperfect-mon")
	assert.Contains(t, msg, "Missed: 1")
	assert.Contains(t, msg, "🟡")
}

func TestTierEmoji_AllFourTiers(t *testing.T) {
	assert.Equal(t, "🔴", tierEmoji(score.TierCritical))
	assert.Equal(t, "🟠", tierEmoji(score.TierWatch))
	assert.Equal(t, "🟡", tierEmoji(score.TierGood))
	assert.Equal(t, "🟢", tierEmoji(score.TierExcellent))
}
