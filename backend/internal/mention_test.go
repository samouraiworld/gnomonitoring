package internal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMentionTag(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"empty", "", "", false},
		{"blank", "   ", "", false},
		{"user id", "123456789012345678", "123456789012345678", false},
		{"user id padded", "  123456789012345678 ", "123456789012345678", false},
		{"role id", "&123456789012345678", "&123456789012345678", false},
		{"pasted user mention", "<@123456789012345678>", "123456789012345678", false},
		{"pasted legacy nickname mention", "<@!123456789012345678>", "123456789012345678", false},
		{"pasted role mention", "<@&123456789012345678>", "&123456789012345678", false},
		{"bare ampersand", "&", "", true},
		{"empty brackets", "<@>", "", true},
		{"username", "bob", "", true},
		{"slack user id", "U01ABCDEF", "", true},
		{"everyone", "@everyone", "", true},
		{"double ampersand", "&&123", "", true},
		{"unclosed bracket", "<@123", "", true},
		{"channel mention", "<#123>", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeMentionTag(c.raw)
			if c.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestRenderAlertDiscordEmbed_RoleMention(t *testing.T) {
	d := AlertData{ChainID: "test12", Level: AlertWarning, Title: "WARNING", Mentions: []string{"111", "&222"}}
	content, _ := RenderAlertDiscordEmbed(d)
	assert.Equal(t, "<@111>\n<@&222>", content)
}

func TestDiscordAllowedMentionsFor_SplitsUsersAndRoles(t *testing.T) {
	got := DiscordAllowedMentionsFor([]string{"111", "&222", "333"})
	assert.Equal(t, []string{}, got.Parse)
	assert.Equal(t, []string{"111", "333"}, got.Users)
	assert.Equal(t, []string{"222"}, got.Roles)
}

func TestRenderAlertSlackBlocks_SkipsRoleMentions(t *testing.T) {
	d := AlertData{ChainID: "test12", Title: "CRITICAL", Mentions: []string{"111", "&222"}}
	blocks := RenderAlertSlackBlocks(d)
	last := blocks[len(blocks)-1]
	require.Equal(t, "context", last.Type)
	assert.Equal(t, "<@111>", last.Elements[0].Text)

	// Only role mentions: no mentions block at all, rather than an empty one.
	blocks = RenderAlertSlackBlocks(AlertData{ChainID: "test12", Title: "CRITICAL", Mentions: []string{"&222"}})
	assert.Len(t, blocks, 1, "expected only the header block, got %+v", blocks)
}

func TestSendDiscordAlertEmbed_PostsAllowedMentions(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	orig := alertHTTPClient
	alertHTTPClient = &http.Client{Timeout: 10 * time.Second}
	defer func() { alertHTTPClient = orig }()

	allowed := DiscordAllowedMentionsFor([]string{"111", "&222"})
	require.NoError(t, SendDiscordAlertEmbed("<@111>\n<@&222>", allowed, DiscordEmbed{Title: "t"}, srv.URL))

	am, ok := captured["allowed_mentions"].(map[string]any)
	require.True(t, ok, "allowed_mentions missing, got: %+v", captured)
	assert.Equal(t, []any{}, am["parse"])
	assert.Equal(t, []any{"111"}, am["users"])
	assert.Equal(t, []any{"222"}, am["roles"])
}

func TestSendDiscordAlertEmbed_NoMentionsStillDisablesParsing(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	orig := alertHTTPClient
	alertHTTPClient = &http.Client{Timeout: 10 * time.Second}
	defer func() { alertHTTPClient = orig }()

	require.NoError(t, SendDiscordAlertEmbed("", DiscordAllowedMentionsFor(nil), DiscordEmbed{Title: "t"}, srv.URL))

	am, ok := captured["allowed_mentions"].(map[string]any)
	require.True(t, ok, "allowed_mentions missing, got: %+v", captured)
	assert.Equal(t, []any{}, am["parse"])
	assert.NotContains(t, am, "users")
	assert.NotContains(t, am, "roles")
}

func TestMentionsForAlert_WarningAndCriticalOnly(t *testing.T) {
	db := testoutils.NewTestDB(t)

	require.NoError(t, database.InsertAlertContact(db, "user1", "val1", "Operator", "111", 7))
	require.NoError(t, database.InsertAlertContact(db, "user1", "val1", "Role", "&222", 7))
	require.NoError(t, database.InsertAlertContact(db, "user1", "val2", "Other validator", "333", 7))
	require.NoError(t, database.InsertAlertContact(db, "user1", "val1", "No tag", "", 7))

	for _, level := range []string{"WARNING", "CRITICAL"} {
		got, err := mentionsForAlert(db, level, "discord", "user1", "val1", 7)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"111", "&222"}, got, "level %s", level)
	}

	for _, level := range []string{"RESOLVED", "INFO", ""} {
		got, err := mentionsForAlert(db, level, "discord", "user1", "val1", 7)
		require.NoError(t, err)
		assert.Empty(t, got, "level %s must not mention anyone", level)
	}

	got, err := mentionsForAlert(db, "CRITICAL", "telegram", "user1", "val1", 7)
	require.NoError(t, err)
	assert.Empty(t, got, "non discord/slack webhooks never carry mentions")

	got, err = mentionsForAlert(db, "CRITICAL", "discord", "user1", "val1", 8)
	require.NoError(t, err)
	assert.Empty(t, got, "contacts are scoped to their linked webhook")
}
