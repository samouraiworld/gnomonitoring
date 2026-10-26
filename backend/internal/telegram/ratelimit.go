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
	// telegramMaxQueueWait is the longest a paced send may queue for its
	// slot. A send that would wait longer fails at once, so a chat flooding
	// the bot with commands, or a long flood-control cooldown, cannot pile
	// up sleeping goroutines.
	telegramMaxQueueWait = time.Minute
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
// telegramMaxAttempts, asks for a wait longer than telegramMaxRetryAfter, or
// when a paced send would queue longer than telegramMaxQueueWait.
var ErrRateLimited = errors.New("telegram: rate limited")

// chatKey scopes per-chat state to one bot: the validator and GovDAO bots
// have their own Telegram limits, and blocking one says nothing of the other.
type chatKey struct {
	token  string
	chatID int64
}

// sendPacer spaces sendMessage calls so each bot stays under Telegram's
// global and per-chat limits, across every goroutine that sends.
type sendPacer struct {
	mu       sync.Mutex
	global   time.Duration
	private  time.Duration
	group    time.Duration
	maxWait  time.Duration
	nextBot  map[string]time.Time
	nextChat map[chatKey]time.Time
	// cooldown is the end of each bot's last 429 wait. Calls already
	// holding an earlier slot check it again right before sending.
	cooldown map[string]time.Time
}

func newSendPacer(global, private, group, maxWait time.Duration) *sendPacer {
	return &sendPacer{
		global: global, private: private, group: group, maxWait: maxWait,
		nextBot: map[string]time.Time{}, nextChat: map[chatKey]time.Time{},
		cooldown: map[string]time.Time{},
	}
}

// telegramPacer paces every sendMessage call of the package.
var telegramPacer = newSendPacer(telegramGlobalInterval, telegramPrivateInterval, telegramGroupInterval, telegramMaxQueueWait)

// reserve books the earliest send slot of token's bot for chatID at or after
// now and returns it. Group and channel chat IDs are negative and get the
// slower group pace. When that slot is more than maxWait away nothing is
// booked and ok is false.
func (p *sendPacer) reserve(now time.Time, token string, chatID int64) (at time.Time, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := chatKey{token, chatID}
	at = now
	if next := p.nextBot[token]; next.After(at) {
		at = next
	}
	if next := p.nextChat[key]; next.After(at) {
		at = next
	}
	if at.Sub(now) > p.maxWait {
		return at, false
	}
	p.nextBot[token] = at.Add(p.global)
	interval := p.private
	if chatID < 0 {
		interval = p.group
	}
	p.nextChat[key] = at.Add(interval)

	if len(p.nextChat) > pacerPruneThreshold {
		for k, next := range p.nextChat {
			if !next.After(now) {
				delete(p.nextChat, k)
			}
		}
	}
	return at, true
}

// backoff holds every paced send of token's bot until at least until, after
// a 429. Telegram does not say whether the limit hit was the chat's or the
// bot's, so the whole bot waits.
func (p *sendPacer) backoff(token string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.nextBot[token].Before(until) {
		p.nextBot[token] = until
	}
	if p.cooldown[token].Before(until) {
		p.cooldown[token] = until
	}
}

// cooldownUntil returns the end of token's current 429 cooldown (zero or
// past when there is none).
func (p *sendPacer) cooldownUntil(token string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cooldown[token]
}

// telegramNow is the send path's clock; tests advance it with telegramSleep.
var telegramNow = time.Now

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

var (
	blockedChatsMu sync.RWMutex
	blockedChats   = map[chatKey]struct{}{}
)

func isChatBlocked(token string, chatID int64) bool {
	blockedChatsMu.RLock()
	defer blockedChatsMu.RUnlock()
	_, ok := blockedChats[chatKey{token, chatID}]
	return ok
}

// markChatBlocked records that the bot cannot write to chatID. It logs only
// the first time, so a broadcast does not log the same chat on every send.
func markChatBlocked(token string, chatID int64, reason string) {
	blockedChatsMu.Lock()
	defer blockedChatsMu.Unlock()
	key := chatKey{token, chatID}
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
	key := chatKey{token, chatID}
	if _, ok := blockedChats[key]; !ok {
		return
	}
	delete(blockedChats, key)
	log.Printf("✅ chat_id=%d reachable again: resuming sends", chatID)
}
