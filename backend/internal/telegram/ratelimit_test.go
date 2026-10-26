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
	telegramPacer = newSendPacer(0, 0, 0, time.Minute)
	os.Exit(m.Run())
}

// usePacer installs p for the duration of the test.
func usePacer(t *testing.T, p *sendPacer) {
	t.Helper()
	restore := telegramPacer
	telegramPacer = p
	t.Cleanup(func() { telegramPacer = restore })
}

// recordSleeps replaces telegramSleep with a recorder that does not wait but
// advances a fake telegramNow clock by each wait. onSleep, when given, runs
// inside every wait, as another goroutine would while this one sleeps.
func recordSleeps(t *testing.T, onSleep ...func()) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var waits []time.Duration
	base, offset := time.Now(), time.Duration(0)
	restoreSleep, restoreNow := telegramSleep, telegramNow
	telegramNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return base.Add(offset)
	}
	telegramSleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			mu.Lock()
			waits = append(waits, d)
			offset += d
			mu.Unlock()
			for _, f := range onSleep {
				f()
			}
		}
		return ctx.Err()
	}
	t.Cleanup(func() { telegramSleep, telegramNow = restoreSleep, restoreNow })
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
		blockedChats = map[chatKey]struct{}{}
		blockedChatsMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// -------------------------------------------------------------------------
// 429: retry_after
// -------------------------------------------------------------------------

// freshPacer installs an unpaced pacer, so a 429 cooldown set by one test
// cannot leak into the next.
func freshPacer(t *testing.T) {
	t.Helper()
	usePacer(t, newSendPacer(0, 0, 0, time.Minute))
}

func TestSendMessage_429WaitsRetryAfterThenRetries(t *testing.T) {
	waits := recordSleeps(t)
	freshPacer(t)
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
	freshPacer(t)
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
	freshPacer(t)
	calls := scriptedTelegram(t, func(int, int64) (int, string) {
		return http.StatusTooManyRequests, tooManyAnswer(int(telegramMaxRetryAfter/time.Second) + 1)
	})

	err := SendMessageTelegram("tok", 42, "hello")
	require.ErrorIs(t, err, ErrRateLimited)
	assert.Len(t, calls(), 1)
	assert.Empty(t, waits())
}

func TestSendMessage_429WaitStopsWithContext(t *testing.T) {
	freshPacer(t)
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
	p := newSendPacer(10*time.Millisecond, time.Second, 3*time.Second, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	slot := func(token string, chatID int64) time.Time {
		at, ok := p.reserve(now, token, chatID, p.queueDeadline(now))
		require.True(t, ok)
		return at
	}

	assert.Equal(t, now, slot("bot", 1), "first send goes at once")
	assert.Equal(t, now.Add(10*time.Millisecond), slot("bot", 2), "another chat waits the global interval")
	assert.Equal(t, now.Add(time.Second), slot("bot", 1), "same private chat waits one second")
	assert.Equal(t, now.Add(time.Second+10*time.Millisecond), slot("bot", -5), "global slot after the previous send")
	assert.Equal(t, now.Add(4*time.Second+10*time.Millisecond), slot("bot", -5), "a group waits three seconds")
	assert.Equal(t, now, slot("other-bot", 1), "another bot has its own limits")

	later := now.Add(time.Minute)
	at, ok := p.reserve(later, "bot", 1, p.queueDeadline(later))
	assert.True(t, ok)
	assert.Equal(t, later, at, "past slots do not delay")
}

func TestSendPacer_QueueFullBooksNothing(t *testing.T) {
	p := newSendPacer(0, time.Second, time.Second, 2*time.Second)
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		_, ok := p.reserve(now, "bot", 1, p.queueDeadline(now))
		require.True(t, ok, "slot %d", i)
	}
	at, ok := p.reserve(now, "bot", 1, p.queueDeadline(now))
	assert.False(t, ok, "a fourth message would wait 3s, over the 2s cap")
	assert.Equal(t, now.Add(3*time.Second), at)
	_, ok = p.reserve(now, "bot", 2, p.queueDeadline(now))
	assert.True(t, ok, "other chats are not held by one chat's backlog")
}

func TestSendPacer_BackoffHoldsWholeBot(t *testing.T) {
	p := newSendPacer(0, 0, 0, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	p.backoff("bot", now.Add(5*time.Second))

	at, ok := p.reserve(now, "bot", 99, p.queueDeadline(now))
	require.True(t, ok)
	assert.Equal(t, now.Add(5*time.Second), at)
	at, _ = p.reserve(now, "other-bot", 99, p.queueDeadline(now))
	assert.Equal(t, now, at)

	p.backoff("bot", now.Add(time.Second))
	at, _ = p.reserve(now, "bot", 98, p.queueDeadline(now))
	assert.Equal(t, now.Add(5*time.Second), at, "a shorter backoff does not shorten the cooldown")
}

func TestSendPacer_PrunesStaleChats(t *testing.T) {
	p := newSendPacer(0, time.Second, time.Second, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	for i := int64(1); i <= pacerPruneThreshold; i++ {
		p.reserve(now, "bot", i, p.queueDeadline(now))
	}
	later := now.Add(time.Hour)
	p.reserve(later, "bot", 0, p.queueDeadline(later))
	assert.Len(t, p.nextChat, 1)
}

func TestSendMessage_429HoldsOtherChatsOfTheBot(t *testing.T) {
	waits := recordSleeps(t)
	freshPacer(t)
	calls := scriptedTelegram(t, func(n int, _ int64) (int, string) {
		if n == 1 {
			return http.StatusTooManyRequests, tooManyAnswer(4)
		}
		return http.StatusOK, okAnswer
	})

	start := telegramNow()
	require.NoError(t, SendMessageTelegram("tok", 1, "a"))
	require.NoError(t, SendMessageTelegram("tok", 2, "b"))
	require.NoError(t, SendMessageTelegram("other-bot", 3, "c"))

	assert.Equal(t, []time.Duration{4 * time.Second}, waits(), "only the retry waited: chat 2 sent after the cooldown")
	assert.Len(t, calls(), 4)
	assert.Equal(t, start.Add(4*time.Second), telegramNow())
}

func TestSendMessage_CooldownReachesCallsAlreadyWaiting(t *testing.T) {
	var once sync.Once
	waits := recordSleeps(t, func() {
		// Another goroutine's 429 lands while this call waits for its slot.
		once.Do(func() { telegramPacer.backoff("tok", telegramNow().Add(5*time.Second)) })
	})
	usePacer(t, newSendPacer(time.Second, 0, 0, time.Minute))
	calls := scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })

	require.NoError(t, SendMessageTelegram("tok", 1, "a"))
	require.NoError(t, SendMessageTelegram("tok", 2, "b"))

	assert.Equal(t, []time.Duration{time.Second, 5 * time.Second}, waits(),
		"chat 2 waited its slot, then the cooldown that started meanwhile")
	assert.Len(t, calls(), 2)
}

func TestEditMessage_CooldownExtendedWhileWaitingIsWaitedToo(t *testing.T) {
	var once sync.Once
	waits := recordSleeps(t, func() {
		once.Do(func() { telegramPacer.backoff("tok", telegramNow().Add(2*time.Second)) })
	})
	freshPacer(t)
	scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })
	telegramPacer.backoff("tok", telegramNow().Add(3*time.Second))

	require.NoError(t, EditMessageTelegramWithMarkup("tok", 5, 1, "x", nil))
	assert.Equal(t, []time.Duration{3 * time.Second, 2 * time.Second}, waits())
}

func TestSendMessage_CallsReleasedByCooldownStaySpaced(t *testing.T) {
	waits := recordSleeps(t)
	usePacer(t, newSendPacer(time.Second, 0, 0, time.Minute))
	calls := scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })
	telegramPacer.backoff("tok", telegramNow().Add(5*time.Second))

	require.NoError(t, SendMessageTelegram("tok", 1, "a"))
	require.NoError(t, SendMessageTelegram("tok", 2, "b"))

	assert.Equal(t, []time.Duration{5 * time.Second, time.Second}, waits())
	got := calls()
	require.Len(t, got, 2)
}

func TestEditMessage_RepeatedCooldownExtensionsAreBounded(t *testing.T) {
	waits := recordSleeps(t, func() {
		telegramPacer.backoff("tok", telegramNow().Add(20*time.Second))
	})
	freshPacer(t)
	calls := scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })
	telegramPacer.backoff("tok", telegramNow().Add(20*time.Second))

	err := EditMessageTelegramWithMarkup("tok", 5, 1, "x", nil)
	require.ErrorIs(t, err, ErrRateLimited)
	assert.Equal(t, []time.Duration{20 * time.Second, 20 * time.Second, 20 * time.Second}, waits(),
		"the whole wait stays within the one-minute queue budget")
	assert.Empty(t, calls())
}

func TestSendMessage_BlockWhileWaitingStopsTheSend(t *testing.T) {
	resetBlockedChats(t)
	recordSleeps(t, func() { markChatBlocked("tok", 8, "test") })
	usePacer(t, newSendPacer(0, time.Second, time.Second, time.Minute))
	calls := scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })

	require.NoError(t, SendMessageTelegram("tok", 8, "a"))
	err := SendMessageTelegram("tok", 8, "b")
	require.ErrorIs(t, err, ErrChatBlocked)
	assert.Len(t, calls(), 1, "the queued message was not sent to the now-blocked chat")
}

func TestEditMessage_WaitsBotCooldown(t *testing.T) {
	waits := recordSleeps(t)
	freshPacer(t)
	scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })
	telegramPacer.backoff("tok", telegramNow().Add(3*time.Second))

	require.NoError(t, EditMessageTelegramWithMarkup("tok", 5, 1, "x", nil))
	assert.Equal(t, []time.Duration{3 * time.Second}, waits())
}

func TestSendMessage_LongCooldownFailsFast(t *testing.T) {
	waits := recordSleeps(t)
	freshPacer(t)
	calls := scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })
	telegramPacer.backoff("tok", telegramNow().Add(time.Hour))

	require.ErrorIs(t, SendMessageTelegram("tok", 5, "x"), ErrRateLimited)
	require.ErrorIs(t, EditMessageTelegramWithMarkup("tok", 5, 1, "x", nil), ErrRateLimited)
	assert.Empty(t, waits())
	assert.Empty(t, calls())
}

func TestSendMessage_QueueFullFailsFast(t *testing.T) {
	recordSleeps(t)
	usePacer(t, newSendPacer(0, time.Minute, time.Minute, time.Second))
	calls := scriptedTelegram(t, func(int, int64) (int, string) { return http.StatusOK, okAnswer })

	require.NoError(t, SendMessageTelegram("tok", 3, "a"))
	err := SendMessageTelegram("tok", 3, "b")
	require.ErrorIs(t, err, ErrRateLimited)
	assert.Len(t, calls(), 1)
}

func TestMsgTelegramGovdao_BroadcastIsPaced(t *testing.T) {
	db := testoutils.NewTestDB(t)
	usePacer(t, newSendPacer(30*time.Millisecond, time.Second, time.Second, time.Minute))
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
	usePacer(t, newSendPacer(0, time.Second, 3*time.Second, time.Minute))
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
