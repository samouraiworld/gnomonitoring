package telegram

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// Telegram Bot API send limits
// (https://core.telegram.org/bots/faq#my-bot-is-hitting-limits-how-do-i-avoid-this):
// about 30 messages per second overall, about 1 per second in one chat, and at
// most 20 per minute in one group. They are package variables so tests can
// shrink them.
var (
	telegramGlobalInterval  = time.Second / 30
	telegramPrivateInterval = time.Second
	telegramGroupInterval   = 3 * time.Second
)

const (
	// telegramMaxAttempts bounds how many times one call is sent when
	// Telegram answers 429.
	telegramMaxAttempts = 3
	// telegramMaxRetryAfter is the longest retry_after we wait for. A longer
	// one fails the call at once instead of stalling a broadcast loop.
	telegramMaxRetryAfter = 30 * time.Second
	// pacerPruneThreshold is the per-chat map size above which stale entries
	// are dropped.
	pacerPruneThreshold = 1024
)

// ErrChatBlocked is returned when the bot cannot write to a chat any more
// (403: the user blocked the bot, or the bot was removed from the group). The
// chat is skipped in memory until it writes to the bot again or the process
// restarts; nothing is changed in the database.
var ErrChatBlocked = errors.New("telegram: bot blocked or removed from chat")

// ErrRateLimited is returned when Telegram still answers 429 after
// telegramMaxAttempts, or asks for a wait longer than telegramMaxRetryAfter.
var ErrRateLimited = errors.New("telegram: rate limited")

// sendPacer spaces sendMessage calls so the bot stays under Telegram's
// global and per-chat limits, across every goroutine that sends.
type sendPacer struct {
	mu       sync.Mutex
	global   time.Duration
	private  time.Duration
	group    time.Duration
	nextAny  time.Time
	nextChat map[int64]time.Time
}

func newSendPacer(global, private, group time.Duration) *sendPacer {
	return &sendPacer{global: global, private: private, group: group, nextChat: map[int64]time.Time{}}
}

// telegramPacer paces every sendMessage call of the package.
var telegramPacer = newSendPacer(telegramGlobalInterval, telegramPrivateInterval, telegramGroupInterval)

// reserve books the earliest send slot for chatID at or after now and returns
// it. Group and channel chat IDs are negative and get the slower group pace.
func (p *sendPacer) reserve(now time.Time, chatID int64) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()

	at := now
	if p.nextAny.After(at) {
		at = p.nextAny
	}
	if next, ok := p.nextChat[chatID]; ok && next.After(at) {
		at = next
	}
	p.nextAny = at.Add(p.global)
	interval := p.private
	if chatID < 0 {
		interval = p.group
	}
	p.nextChat[chatID] = at.Add(interval)

	if len(p.nextChat) > pacerPruneThreshold {
		for id, next := range p.nextChat {
			if !next.After(now) {
				delete(p.nextChat, id)
			}
		}
	}
	return at
}

// telegramSleep waits for d or until ctx is done. Tests replace it to record
// the waits instead of sleeping.
var telegramSleep = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// blockedChatKey scopes a blocked chat to one bot: blocking the validator bot
// says nothing about the GovDAO bot.
type blockedChatKey struct {
	token  string
	chatID int64
}

var (
	blockedChatsMu sync.RWMutex
	blockedChats   = map[blockedChatKey]struct{}{}
)

func isChatBlocked(token string, chatID int64) bool {
	blockedChatsMu.RLock()
	defer blockedChatsMu.RUnlock()
	_, ok := blockedChats[blockedChatKey{token, chatID}]
	return ok
}

// markChatBlocked records that the bot cannot write to chatID. It logs only
// the first time, so a broadcast does not log the same chat on every send.
func markChatBlocked(token string, chatID int64, reason string) {
	blockedChatsMu.Lock()
	defer blockedChatsMu.Unlock()
	key := blockedChatKey{token, chatID}
	if _, ok := blockedChats[key]; ok {
		return
	}
	blockedChats[key] = struct{}{}
	log.Printf("🚫 chat_id=%d cannot be reached (%s): skipping it until it writes to the bot again", chatID, reason)
}

// unblockChat lifts the block once the chat shows the bot is welcome again.
func unblockChat(token string, chatID int64) {
	blockedChatsMu.Lock()
	defer blockedChatsMu.Unlock()
	key := blockedChatKey{token, chatID}
	if _, ok := blockedChats[key]; !ok {
		return
	}
	delete(blockedChats, key)
	log.Printf("✅ chat_id=%d reachable again: resuming sends", chatID)
}
