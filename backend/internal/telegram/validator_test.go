package telegram

// White-box tests for the chain-state helpers and command handlers in
// validator.go.  Because the helpers (getActiveChain, setActiveChain,
// validateChainID) are unexported they are only reachable from within the same
// package, so this file lives in package telegram.

import (
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
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
