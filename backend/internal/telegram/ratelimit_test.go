package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

// TestMain disables send pacing for the package's tests, which otherwise
// would wait a second between two messages to the same chat. Pacing tests
// install their own pacer.
func TestMain(m *testing.M) {
	telegramPacer = newSendPacer(0, 0, 0)
	os.Exit(m.Run())
}

// usePacer installs p for the duration of the test.
func usePacer(t *testing.T, p *sendPacer) {
	t.Helper()
	restore := telegramPacer
	telegramPacer = p
	t.Cleanup(func() { telegramPacer = restore })
}

// recordSleeps replaces telegramSleep with a recorder that does not wait.
func recordSleeps(t *testing.T) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var waits []time.Duration
	restore := telegramSleep
	telegramSleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			mu.Lock()
			waits = append(waits, d)
			mu.Unlock()
		}
		return ctx.Err()
	}
	t.Cleanup(func() { telegramSleep = restore })
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), waits...)
	}
}

// scriptedTelegram serves the given answers in order (the last one repeats)
// and records the chat_id and arrival time of every request.
type scriptedCall struct {
	chatID int64
	at     time.Time
}

func scriptedTelegram(t *testing.T, answer func(n int, chatID int64) (int, string)) func() []scriptedCall {
	t.Helper()
	var mu sync.Mutex
	var calls []scriptedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ChatID int64 `json:"chat_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, scriptedCall{chatID: body.ChatID, at: time.Now()})
		n := len(calls)
		mu.Unlock()
		status, payload := answer(n, body.ChatID)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	restore := telegramAPIBaseURL
	telegramAPIBaseURL = srv.URL
	t.Cleanup(func() {
		telegramAPIBaseURL = restore
		srv.Close()
	})
	return func() []scriptedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]scriptedCall(nil), calls...)
	}
}

const (
	okAnswer      = `{"ok":true}`
	blockedAnswer = `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`
)

func tooManyAnswer(retryAfter int) string {
	b, _ := json.Marshal(map[string]any{
		"ok":          false,
		"error_code":  429,
		"description": "Too Many Requests",
		"parameters":  map[string]int{"retry_after": retryAfter},
	})
	return string(b)
}

// resetBlockedChats clears the package-wide blocked set before and after a test.
func resetBlockedChats(t *testing.T) {
	t.Helper()
	clear := func() {
		blockedChatsMu.Lock()
		blockedChats = map[blockedChatKey]struct{}{}
		blockedChatsMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// -------------------------------------------------------------------------
// 429: retry_after
// -------------------------------------------------------------------------

func TestSendMessage_429WaitsRetryAfterThenRetries(t *testing.T) {
	waits := recordSleeps(t)
	calls := scriptedTelegram(t, func(n int, _ int64) (int, string) {
		if n == 1 {
			return http.StatusTooManyRequests, tooManyAnswer(2)
		}
		return http.StatusOK, okAnswer
	})

	require.NoError(t, SendMessageTelegram("tok", 42, "hello"))
	assert.Len(t, calls(), 2)
	assert.Equal(t, []time.Duration{2 * time.Second}, waits())
}

func TestSendMessage_429GivesUpAfterMaxAttempts(t *testing.T) {
	waits := recordSleeps(t)
	calls := scriptedTelegram(t, func(int, int64) (int, string) {
		return http.StatusTooManyRequests, tooManyAnswer(1)
	})

	err := SendMessageTelegram("tok", 42, "hello")
	require.ErrorIs(t, err, ErrRateLimited)
	assert.Len(t, calls(), telegramMaxAttempts)
	assert.Len(t, waits(), telegramMaxAttempts-1)
}

func TestSendMessage_429RetryAfterTooLongFailsAtOnce(t *testing.T) {
	waits := recordSleeps(t)
	calls := scriptedTelegram(t, func(int, int64) (int, string) {
		return http.StatusTooManyRequests, tooManyAnswer(int(telegramMaxRetryAfter/time.Second) + 1)
	})

	err := SendMessageTelegram("tok", 42, "hello")
	require.ErrorIs(t, err, ErrRateLimited)
	assert.Len(t, calls(), 1)
	assert.Empty(t, waits())
}

func TestSendMessage_429WaitStopsWithContext(t *testing.T) {
	scriptedTelegram(t, func(int, int64) (int, string) {
		return http.StatusTooManyRequests, tooManyAnswer(5)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := callTelegram(ctx, "tok", "sendMessage", 42, false, map[string]any{"chat_id": 42})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}

// -------------------------------------------------------------------------
// 403: blocked chat
// -------------------------------------------------------------------------

func TestSendMessage_403ReturnsErrChatBlockedAndSkipsLaterSends(t *testing.T) {
	resetBlockedChats(t)
	calls := scriptedTelegram(t, func(_ int, chatID int64) (int, string) {
		if chatID == 7 {
			return http.StatusForbidden, blockedAnswer
		}
		return http.StatusOK, okAnswer
	})

	err := SendMessageTelegram("tok", 7, "first")
	require.ErrorIs(t, err, ErrChatBlocked)
	err = SendMessageTelegram("tok", 7, "second")
	require.ErrorIs(t, err, ErrChatBlocked)
	assert.Len(t, calls(), 1, "a blocked chat is not sent to again")

	// Scoped per bot: the other bot still tries.
	require.ErrorIs(t, SendMessageTelegram("other-bot", 7, "x"), ErrChatBlocked)
	assert.Len(t, calls(), 2)

	// The chat writes to the bot again: sends resume.
	unblockChat("tok", 7)
	require.ErrorIs(t, SendMessageTelegram("tok", 7, "third"), ErrChatBlocked)
	assert.Len(t, calls(), 3)
}

func TestTrackBotMembership(t *testing.T) {
	resetBlockedChats(t)
	upd := func(status string) *chatMemberUpd {
		u := &chatMemberUpd{Chat: chat{ID: -100}}
		u.NewChatMember.Status = status
		return u
	}

	trackBotMembership("tok", upd("kicked"))
	assert.True(t, isChatBlocked("tok", -100))
	trackBotMembership("tok", upd("member"))
	assert.False(t, isChatBlocked("tok", -100))
	trackBotMembership("tok", upd("left"))
	assert.True(t, isChatBlocked("tok", -100))
	trackBotMembership("tok", upd("administrator"))
	assert.False(t, isChatBlocked("tok", -100))
}

func TestMsgTelegramGovdao_BlockedChatDoesNotStopBroadcast(t *testing.T) {
	resetBlockedChats(t)
	db := testoutils.NewTestDB(t)
	calls := scriptedTelegram(t, func(_ int, chatID int64) (int, string) {
		if chatID == 102 {
			return http.StatusForbidden, blockedAnswer
		}
		return http.StatusOK, okAnswer
	})
	for _, chatID := range []int64{101, 102, 103} {
		_, err := database.InsertChatID(db, chatID, "govdao", "gnoland1")
		require.NoError(t, err)
	}
	chains := []string{"gnoland1"}

	require.NoError(t, MsgTelegramGovdao("one", "gnoland1", "tok", "gnoland1", chains, db))
	require.NoError(t, MsgTelegramGovdao("two", "gnoland1", "tok", "gnoland1", chains, db))

	var got []int64
	for _, c := range calls() {
		got = append(got, c.chatID)
	}
	assert.Equal(t, []int64{101, 102, 103, 101, 103}, got, "102 answered 403 once, then is skipped")
}

func TestMsgTelegramAlert_SkipsBlockedChat(t *testing.T) {
	resetBlockedChats(t)
	db := testoutils.NewTestDB(t)
	sent := fakeTelegram(t)
	for _, chatID := range []int64{201, 202} {
		_, err := database.InsertChatID(db, chatID, "validator", "gnoland1")
		require.NoError(t, err)
		require.NoError(t, db.Create(&database.TelegramValidatorSub{
			ChatID: chatID, ChainID: "gnoland1", Addr: "g1val", Moniker: "val", Activate: true,
		}).Error)
	}
	markChatBlocked("tok", 202, "test")

	require.NoError(t, MsgTelegramAlert("alert", "g1val", "gnoland1", "tok", "validator", db))
	assert.Equal(t, []string{"alert"}, sent())
}

// -------------------------------------------------------------------------
// Pacing
// -------------------------------------------------------------------------

func TestSendPacer_Reserve(t *testing.T) {
	p := newSendPacer(10*time.Millisecond, time.Second, 3*time.Second)
	now := time.Unix(1_700_000_000, 0)

	assert.Equal(t, now, p.reserve(now, 1), "first send goes at once")
	assert.Equal(t, now.Add(10*time.Millisecond), p.reserve(now, 2), "another chat waits the global interval")
	assert.Equal(t, now.Add(time.Second), p.reserve(now, 1), "same private chat waits one second")
	assert.Equal(t, now.Add(time.Second+10*time.Millisecond), p.reserve(now, -5), "global slot after the previous send")
	assert.Equal(t, now.Add(4*time.Second+10*time.Millisecond), p.reserve(now, -5), "a group waits three seconds")

	later := now.Add(time.Minute)
	assert.Equal(t, later, p.reserve(later, 1), "past slots do not delay")
}

func TestSendPacer_PrunesStaleChats(t *testing.T) {
	p := newSendPacer(0, time.Second, time.Second)
	now := time.Unix(1_700_000_000, 0)
	for i := int64(1); i <= pacerPruneThreshold; i++ {
		p.reserve(now, i)
	}
	p.reserve(now.Add(time.Hour), 0)
	assert.Len(t, p.nextChat, 1)
}

func TestMsgTelegramGovdao_BroadcastIsPaced(t *testing.T) {
	db := testoutils.NewTestDB(t)
	usePacer(t, newSendPacer(30*time.Millisecond, time.Second, time.Second))
	calls := scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })
	for _, chatID := range []int64{301, 302, 303} {
		_, err := database.InsertChatID(db, chatID, "govdao", "gnoland1")
		require.NoError(t, err)
	}

	require.NoError(t, MsgTelegramGovdao("notice", "gnoland1", "tok", "gnoland1", []string{"gnoland1"}, db))

	got := calls()
	require.Len(t, got, 3)
	for i := 1; i < len(got); i++ {
		gap := got[i].at.Sub(got[i-1].at)
		assert.GreaterOrEqual(t, gap, 25*time.Millisecond, "send %d came %s after the previous one", i, gap)
	}
}

func TestSendMessage_SameChatIsPaced(t *testing.T) {
	waits := recordSleeps(t)
	usePacer(t, newSendPacer(0, time.Second, 3*time.Second))
	scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })

	require.NoError(t, SendMessageTelegram("tok", 9, "a"))
	require.NoError(t, SendMessageTelegram("tok", 9, "b"))

	got := waits()
	require.Len(t, got, 1)
	assert.InDelta(t, float64(time.Second), float64(got[0]), float64(100*time.Millisecond))
}

func TestCallTelegram_TransportErrorHidesToken(t *testing.T) {
	restore := telegramAPIBaseURL
	telegramAPIBaseURL = "http://127.0.0.1:1"
	t.Cleanup(func() { telegramAPIBaseURL = restore })

	err := SendMessageTelegram("secret-token", 1, "x")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.False(t, errors.Is(err, ErrChatBlocked))
}
