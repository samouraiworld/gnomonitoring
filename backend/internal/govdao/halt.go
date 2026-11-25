package govdao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	ctypes "github.com/gnolang/gno/tm2/pkg/bft/rpc/core/types"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/rpcpool"
	"github.com/samouraiworld/gnomonitoring/backend/internal/telegram"
	"gorm.io/gorm"
)

// A chain halt is scheduled on gno.land by a GovDAO proposal built with
// gno.land/r/sys/params.NewSetHaltRequest(height, minVersion). Once executed,
// it writes two node params, which are the source of truth the node itself
// reads (its EndBlocker halts at exactly that height):
//
//	node:p:halt_height       int64, 0 = no halt
//	node:p:halt_min_version  string, "" = no minimum version
//
// The params are never reset after the halt: once the chain has restarted on
// the new binary, halt_height still holds the past height. A halt is therefore
// "scheduled" only while halt_height is above the latest block height. A later
// proposal moves it (another future height) or cancels it (0, "").
const (
	haltHeightParamPath     = "params/node:p:halt_height"
	haltMinVersionParamPath = "params/node:p:halt_min_version"
)

// Halt bookkeeping lives in alert_logs under addr = haltAlertAddr, one level
// per kind of notice, with start_height = the halt height and moniker = the
// required version. Every validator-scoped query filters on its own levels, so
// these rows never reach the score, the missed-block dedup or the alert counts.
const (
	haltAlertAddr = "halt"

	LevelHaltScheduled     = "HALT_SCHEDULED"
	LevelHaltCancelled     = "HALT_CANCELLED"
	LevelHaltReminder      = "HALT_REMINDER"
	LevelHaltFinalReminder = "HALT_FINAL_REMINDER"
	LevelHaltResumed       = "HALT_RESUMED"
)

const (
	// haltPollInterval is how often each chain's halt params are read. Short
	// enough for the final reminder (default 5 minutes) to go out on time.
	haltPollInterval = 30 * time.Second

	// haltPaceBlocks is the span the block pace is measured over: short enough
	// to follow a change of block-time regime, long enough to smooth out slow
	// rounds.
	haltPaceBlocks int64 = 3000
	// haltPaceFallbackBlocks is the span tried when the node no longer has
	// the block haltPaceBlocks back (pruned history).
	haltPaceFallbackBlocks int64 = 100

	defaultHaltReminderMinutes      = 60
	defaultHaltFinalReminderMinutes = 5
	// maxHaltReminderMinutes caps the admin-set lead times (one week), so a
	// huge value cannot overflow time.Duration.
	maxHaltReminderMinutes = 7 * 24 * 60

	// maxHaltVersionLen bounds the on-chain version string shown in notices.
	maxHaltVersionLen = 64
)

// haltVersionPattern is what a displayable halt_min_version may contain: the
// node accepts vMAJOR.MINOR.PATCH and betanet's chain/gnolandMAJOR.MINOR, and
// any other value is compared by equality. The version is set on chain by a
// proposal, so it is untrusted text: anything outside this set (markup,
// mentions, newlines) is never put in a message.
var haltVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._+/-]+$`)

// haltRPC is the part of the chain's RPC pool the halt watcher reads.
type haltRPC interface {
	Status() (*ctypes.ResultStatus, error)
	ABCIQueryWithOptions(path string, data []byte, opts rpcclient.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error)
	Block(height *int64) (*ctypes.ResultBlock, error)
}

// haltObservation is what the chain reports at one moment.
type haltObservation struct {
	HaltHeight   int64
	MinVersion   string
	LatestHeight int64
	LatestTime   time.Time
}

// scheduled reports whether the chain still has a halt ahead of it, or is
// halted at it right now (the head is the halt height).
func (o haltObservation) scheduled() bool {
	return o.HaltHeight != 0 && o.HaltHeight >= o.LatestHeight
}

// readHaltParam returns the raw JSON value stored under a node param at
// height, or nil when the param was never set. Both params and the head are
// read at the same height, so a block committed between the calls, or a pool
// failover to a node behind, cannot pair a head with params of another state:
// a node without that height answers an error and the poll is skipped.
func readHaltParam(c haltRPC, path string, height int64) ([]byte, error) {
	res, err := c.ABCIQueryWithOptions(path, nil, rpcclient.ABCIQueryOptions{Height: height})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("abci_query %s: empty result", path)
	}
	if res.Response.Error != nil {
		return nil, fmt.Errorf("abci_query %s: %v", path, res.Response.Error)
	}
	data := strings.TrimSpace(string(res.Response.Data))
	if data == "" || data == "null" {
		return nil, nil
	}
	return []byte(data), nil
}

// parseHaltHeight decodes the amino JSON of an int64 param: a quoted decimal
// ("1651500"), or a bare number. A param never set reads as 0.
func parseHaltHeight(raw []byte) (int64, error) {
	if raw == nil {
		return 0, nil
	}
	s := string(raw)
	var quoted string
	if err := json.Unmarshal(raw, &quoted); err == nil {
		s = quoted
	}
	h, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("halt_height %q: %w", string(raw), err)
	}
	if h < 0 {
		return 0, fmt.Errorf("halt_height %d: negative", h)
	}
	return h, nil
}

// parseHaltMinVersion decodes the amino JSON of a string param. A value that
// is not a plain version (see haltVersionPattern) or longer than
// maxHaltVersionLen is replaced by a fixed placeholder, so on-chain text never
// reaches a message as is.
func parseHaltMinVersion(raw []byte) (string, error) {
	if raw == nil {
		return "", nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("halt_min_version: %w", err)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if len(v) > maxHaltVersionLen || !haltVersionPattern.MatchString(v) {
		return invalidHaltVersion, nil
	}
	return v, nil
}

// invalidHaltVersion stands for a halt_min_version that cannot be displayed.
const invalidHaltVersion = "(unreadable version)"

// observeHalt reads the chain head, then the halt params at that height.
func observeHalt(c haltRPC) (haltObservation, error) {
	var obs haltObservation
	st, err := c.Status()
	if err != nil {
		return obs, fmt.Errorf("status: %w", err)
	}
	if st == nil {
		return obs, errors.New("status: empty result")
	}
	if st.SyncInfo.CatchingUp {
		// A syncing node sees an old state: a past halt as still ahead, a
		// scheduled one as not set yet.
		return obs, errors.New("node is catching up")
	}
	obs.LatestHeight = st.SyncInfo.LatestBlockHeight
	obs.LatestTime = st.SyncInfo.LatestBlockTime
	if obs.LatestHeight < 1 {
		return obs, fmt.Errorf("no block yet (height %d)", obs.LatestHeight)
	}

	raw, err := readHaltParam(c, haltHeightParamPath, obs.LatestHeight)
	if err != nil {
		return obs, err
	}
	if obs.HaltHeight, err = parseHaltHeight(raw); err != nil {
		return obs, err
	}
	raw, err = readHaltParam(c, haltMinVersionParamPath, obs.LatestHeight)
	if err != nil {
		return obs, err
	}
	if obs.MinVersion, err = parseHaltMinVersion(raw); err != nil {
		return obs, err
	}
	return obs, nil
}

// blockPace returns the average time per block over the haltPaceBlocks before
// the head described by obs, or over haltPaceFallbackBlocks when the node no
// longer has the older block.
func blockPace(c haltRPC, obs haltObservation) (time.Duration, error) {
	pace, err := blockPaceOver(c, obs, haltPaceBlocks)
	if err != nil {
		if fallback, ferr := blockPaceOver(c, obs, haltPaceFallbackBlocks); ferr == nil {
			return fallback, nil
		}
	}
	return pace, err
}

func blockPaceOver(c haltRPC, obs haltObservation, blocks int64) (time.Duration, error) {
	span := min(blocks, obs.LatestHeight-1)
	if span < 1 {
		return 0, fmt.Errorf("chain too short to measure a pace (height %d)", obs.LatestHeight)
	}
	from := obs.LatestHeight - span
	b, err := c.Block(&from)
	if err != nil {
		return 0, fmt.Errorf("block %d: %w", from, err)
	}
	if b == nil || b.Block == nil {
		return 0, fmt.Errorf("block %d: empty result", from)
	}
	elapsed := obs.LatestTime.Sub(b.Block.Header.Time)
	if elapsed <= 0 {
		return 0, fmt.Errorf("non-increasing block times between %d and %d", from, obs.LatestHeight)
	}
	return elapsed / time.Duration(span), nil
}

// haltRecord is one halt row of alert_logs.
type haltRecord struct {
	ID      uint
	Level   string
	Height  int64
	Version string
	// ObservedAt is the chain head the notice was decided at (end_height).
	ObservedAt int64
	SentAt     time.Time
}

// haltState is what was already announced for a chain: the latest
// announcement (scheduled or cancelled) and the notices sent since it.
type haltState struct {
	Announcement  *haltRecord
	Reminded      bool
	FinalReminded bool
	Resumed       bool
	// LastObserved is the highest chain head any halt notice was decided at.
	// An observation below it comes from a node behind and is ignored.
	LastObserved int64
}

// loadHaltState rebuilds a chain's haltState from alert_logs, so a restart
// neither repeats a notice nor forgets a scheduled halt.
func loadHaltState(db *gorm.DB, chainID string) (haltState, error) {
	var st haltState
	var ann []database.AlertLog
	if err := db.Where("chain_id = ? AND addr = ? AND level IN ?",
		chainID, haltAlertAddr, []string{LevelHaltScheduled, LevelHaltCancelled}).
		Order("id DESC").Limit(1).Find(&ann).Error; err != nil {
		return st, fmt.Errorf("load halt announcement: %w", err)
	}
	if len(ann) == 0 {
		return st, nil
	}
	a := ann[0]
	st.Announcement = &haltRecord{ID: a.ID, Level: a.Level, Height: a.StartHeight, Version: a.Moniker,
		ObservedAt: a.EndHeight, SentAt: a.SentAt}

	if err := db.Model(&database.AlertLog{}).
		Where("chain_id = ? AND addr = ?", chainID, haltAlertAddr).
		Select("COALESCE(MAX(end_height), 0)").Scan(&st.LastObserved).Error; err != nil {
		return st, fmt.Errorf("load halt head: %w", err)
	}

	var levels []string
	if err := db.Model(&database.AlertLog{}).
		Where("chain_id = ? AND addr = ? AND start_height = ? AND id > ? AND level IN ?",
			chainID, haltAlertAddr, a.StartHeight, a.ID,
			[]string{LevelHaltReminder, LevelHaltFinalReminder, LevelHaltResumed}).
		Distinct().Pluck("level", &levels).Error; err != nil {
		return st, fmt.Errorf("load halt notices: %w", err)
	}
	st.Reminded = slices.Contains(levels, LevelHaltReminder)
	st.FinalReminded = slices.Contains(levels, LevelHaltFinalReminder)
	st.Resumed = slices.Contains(levels, LevelHaltResumed)
	return st, nil
}

// haltNoticeKind names the notice a halt check decided to send.
type haltNoticeKind int

const (
	haltNoticeScheduled haltNoticeKind = iota + 1
	haltNoticeMoved
	haltNoticeVersionChanged
	haltNoticeCancelled
	haltNoticeReminder
	haltNoticeFinalReminder
	haltNoticeResumed
)

// haltNotice is one notice to send, and the alert_logs rows recording it.
type haltNotice struct {
	Kind       haltNoticeKind
	Height     int64
	Version    string
	PrevHeight int64  // moved / version changed: the height announced before
	PrevVer    string // version changed: the version announced before
	Remaining  time.Duration
	HasETA     bool
	Pace       time.Duration
	Obs        haltObservation
	// Levels are the alert_logs levels to write, the first one being the
	// notice itself. An announcement already inside a reminder window also
	// records that reminder, so it is not sent again right after.
	Levels []string
}

// haltThresholds are the reminder lead times, read from admin_config.
type haltThresholds struct {
	Reminder      time.Duration
	FinalReminder time.Duration
}

// loadHaltThresholds reads the reminder lead times per use, like the other
// admin_config thresholds. A non-positive value disables that reminder.
func loadHaltThresholds(db *gorm.DB) haltThresholds {
	minutes := func(key string, def int) time.Duration {
		return time.Duration(min(database.GetAdminConfigInt(db, key, def), maxHaltReminderMinutes)) * time.Minute
	}
	return haltThresholds{
		Reminder:      minutes("halt_reminder_minutes", defaultHaltReminderMinutes),
		FinalReminder: minutes("halt_final_reminder_minutes", defaultHaltFinalReminderMinutes),
	}
}

// decideHalt compares what the chain reports with what was already announced
// and returns the notice to send, if any. pace is the average block time, or
// 0 when it could not be measured (no ETA, no reminder).
func decideHalt(obs haltObservation, st haltState, pace time.Duration, th haltThresholds) *haltNotice {
	if obs.LatestHeight < st.LastObserved {
		// A node behind the one the last notice was decided on: its state
		// may predate a proposal already announced.
		return nil
	}
	ann := st.Announcement
	pending := ann != nil && ann.Level == LevelHaltScheduled && !st.Resumed

	if pending && obs.LatestHeight > ann.Height {
		// The chain went past the announced halt: it halted at that height and
		// restarted on the upgraded binary. Reported before any newer halt, so
		// a missed resume is not read as a move.
		return &haltNotice{Kind: haltNoticeResumed, Height: ann.Height, Version: ann.Version, Obs: obs,
			Levels: []string{LevelHaltResumed}}
	}

	if obs.scheduled() {
		n := &haltNotice{Height: obs.HaltHeight, Version: obs.MinVersion, Obs: obs, Pace: pace}
		if blocks := obs.HaltHeight - obs.LatestHeight; pace > 0 && blocks <= math.MaxInt64/int64(pace) {
			n.Remaining = time.Duration(blocks) * pace
			n.HasETA = true
		}
		switch {
		case !pending:
			n.Kind = haltNoticeScheduled
		case ann.Height != obs.HaltHeight:
			n.Kind = haltNoticeMoved
			n.PrevHeight = ann.Height
		case ann.Version != obs.MinVersion:
			n.Kind = haltNoticeVersionChanged
			n.PrevHeight = ann.Height
			n.PrevVer = ann.Version
		default:
			// Already announced as is: only the reminders are left.
			if !n.HasETA {
				return nil
			}
			switch {
			case th.FinalReminder > 0 && n.Remaining <= th.FinalReminder && !st.FinalReminded:
				n.Kind = haltNoticeFinalReminder
				n.Levels = []string{LevelHaltFinalReminder}
				if !st.Reminded {
					n.Levels = append(n.Levels, LevelHaltReminder)
				}
			case th.Reminder > 0 && n.Remaining <= th.Reminder && !st.Reminded:
				n.Kind = haltNoticeReminder
				n.Levels = []string{LevelHaltReminder}
			default:
				return nil
			}
			return n
		}
		n.Levels = []string{LevelHaltScheduled}
		if n.HasETA {
			if th.Reminder > 0 && n.Remaining <= th.Reminder {
				n.Levels = append(n.Levels, LevelHaltReminder)
			}
			if th.FinalReminder > 0 && n.Remaining <= th.FinalReminder {
				n.Levels = append(n.Levels, LevelHaltFinalReminder)
			}
		}
		return n
	}

	if !pending {
		return nil
	}
	if obs.HaltHeight == ann.Height {
		// Halted at the announced height, waiting for the upgrade.
		return nil
	}
	if obs.HaltHeight != 0 {
		// Moved to a height the chain already reached between two polls (a
		// later vote may set any height above the current block). Announce
		// the move, so the resume is reported against the right height.
		return &haltNotice{Kind: haltNoticeMoved, Height: obs.HaltHeight, Version: obs.MinVersion, Obs: obs,
			PrevHeight: ann.Height, Levels: []string{LevelHaltScheduled}}
	}
	// The announced halt is no longer set and the chain has not reached it.
	return &haltNotice{Kind: haltNoticeCancelled, Height: ann.Height, Version: ann.Version, Obs: obs,
		Levels: []string{LevelHaltCancelled}}
}

// formatHaltDuration renders a duration as "5h 12m", "12m" or "40s".
func formatHaltDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	mins := int(d % time.Hour / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// versionLine names the minimum version the node checks when it restarts
// after the halt. Any later release also passes the check.
func versionLine(v string) string {
	if v == "" {
		return "Minimum version to resume: none set"
	}
	return "Minimum version to resume: " + v
}

// etaLines describes when the halt is expected.
func (n *haltNotice) etaLines() []string {
	blocksLeft := n.Height - n.Obs.LatestHeight
	if blocksLeft <= 0 {
		return []string{fmt.Sprintf("Halt height: %d (already reached, current %d)", n.Height, n.Obs.LatestHeight)}
	}
	lines := []string{fmt.Sprintf("Halt height: %d (current %d, %d blocks left)", n.Height, n.Obs.LatestHeight, blocksLeft)}
	if n.HasETA {
		at := n.Obs.LatestTime.Add(n.Remaining).UTC()
		lines = append(lines, fmt.Sprintf("Estimated time to halt: ~%s (around %s, at %.2fs/block)",
			formatHaltDuration(n.Remaining), at.Format("2006-01-02 15:04 MST"), n.Pace.Seconds()))
	} else {
		lines = append(lines, "Estimated time to halt: unknown (block time unavailable)")
	}
	return lines
}

// lines renders the notice as a title and body lines, shared by every
// channel.
func (n *haltNotice) lines() (string, []string) {
	ver := versionLine(n.Version)
	switch n.Kind {
	case haltNoticeScheduled:
		advice := "Have a binary meeting the minimum version ready to restart your node after the halt."
		if n.Version == "" {
			advice = "Have the upgraded binary ready to restart your node after the halt."
		}
		return "⏸️ Chain halt scheduled by GovDAO", append(n.etaLines(), ver, advice)
	case haltNoticeMoved:
		return fmt.Sprintf("⏸️ Chain halt moved by GovDAO: height %d → %d", n.PrevHeight, n.Height),
			append(n.etaLines(), ver)
	case haltNoticeVersionChanged:
		prev := n.PrevVer
		if prev == "" {
			prev = "none"
		}
		next := n.Version
		if next == "" {
			next = "none"
		}
		return fmt.Sprintf("⏸️ Chain halt minimum version changed by GovDAO: %s → %s", prev, next),
			append(n.etaLines(), ver)
	case haltNoticeReminder, haltNoticeFinalReminder:
		if n.Height <= n.Obs.LatestHeight {
			return fmt.Sprintf("⏸️ Chain reached the halt height %d", n.Height), append(n.etaLines(), ver)
		}
		return fmt.Sprintf("⏰ Chain halt in ~%s", formatHaltDuration(n.Remaining)),
			append(n.etaLines(), ver)
	case haltNoticeCancelled:
		return fmt.Sprintf("▶️ Scheduled chain halt at height %d cancelled by GovDAO", n.Height),
			[]string{fmt.Sprintf("Current height: %d", n.Obs.LatestHeight)}
	case haltNoticeResumed:
		return fmt.Sprintf("✅ Chain resumed after the halt at height %d", n.Height),
			[]string{fmt.Sprintf("Current height: %d", n.Obs.LatestHeight), ver}
	}
	return "", nil
}

// formatHaltPlain renders the Discord/Slack text (SendInfoGovdao prefixes the
// chain ID).
func formatHaltPlain(n *haltNotice) string {
	title, body := n.lines()
	return "--- \n" + title + "\n" + strings.Join(body, "\n")
}

// formatHaltTelegram renders the Telegram (HTML parse mode) text, escaping
// every value.
func formatHaltTelegram(chainID string, n *haltNotice) string {
	title, body := n.lines()
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] <b>%s</b>\n", html.EscapeString(chainID), html.EscapeString(title))
	for _, l := range body {
		b.WriteString(html.EscapeString(l))
		b.WriteString("\n")
	}
	return b.String()
}

// sendHaltNotice dispatches n to the GovDAO subscribers of chainID: every
// GovDAO webhook, and the GovDAO Telegram chats following that chain.
func sendHaltNotice(db *gorm.DB, chainID string, n *haltNotice) {
	if err := internal.SendInfoGovdao(chainID, formatHaltPlain(n), db); err != nil {
		log.Printf("[halt][%s] SendInfoGovdao error: %v", chainID, err)
	}

	internal.ConfigMu.Lock()
	token, defaultChain := internal.Config.TokenTelegramGovdao, internal.Config.DefaultChain
	enabledChains := slices.Clone(internal.EnabledChains)
	internal.ConfigMu.Unlock()

	if err := telegram.MsgTelegramGovdao(formatHaltTelegram(chainID, n), chainID, token, defaultChain, enabledChains, db); err != nil {
		log.Printf("[halt][%s] MsgTelegramGovdao error: %v", chainID, err)
	}
}

// recordHaltNotice writes n's alert_logs rows in one transaction, so a notice
// is never half-recorded. The notice itself is stored with skipped = true
// (dispatched); a reminder folded into an announcement with skipped = false
// (recorded, not sent on its own). end_height holds the chain head the notice
// was decided at.
func recordHaltNotice(db *gorm.DB, chainID string, n *haltNotice, msg string, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for i, level := range n.Levels {
			// Strictly increasing sent_at keeps the rows of one notice in order.
			at := now.Add(time.Duration(i) * time.Microsecond)
			if err := database.InsertAlertlog(tx, chainID, haltAlertAddr, n.Version, level, n.Height, n.Obs.LatestHeight, i == 0, at, msg); err != nil {
				return fmt.Errorf("record %s: %w", level, err)
			}
		}
		return nil
	})
}

// Seams for tests.
var (
	haltClientForChain = func(chainID string) (haltRPC, error) {
		pool, ok := rpcpool.Get(chainID)
		if !ok {
			return nil, fmt.Errorf("no RPC pool registered for chain %q", chainID)
		}
		return pool, nil
	}
	notifyHalt = sendHaltNotice
)

// confirmResumed checks that the chain really halted at the announced height
// before it is reported as resumed: if gnomonitoring missed a later vote that
// moved or cancelled that halt, the chain went past it without stopping. The
// param is read at the halt height itself; on a node that no longer has that
// state, the resume is reported as observed.
func confirmResumed(c haltRPC, n *haltNotice) *haltNotice {
	raw, err := readHaltParam(c, haltHeightParamPath, n.Height)
	if err != nil {
		return n
	}
	h, err := parseHaltHeight(raw)
	if err != nil || h == n.Height {
		return n
	}
	// The halt at n.Height was replaced before the chain reached it.
	return &haltNotice{Kind: haltNoticeCancelled, Height: n.Height, Version: n.Version, Obs: n.Obs,
		Levels: []string{LevelHaltCancelled}}
}

// checkChainHalt runs one halt check for chainID. It returns the error of
// reading the chain, which the caller logs; other failures are logged here.
func checkChainHalt(db *gorm.DB, chainID string, c haltRPC, now time.Time) error {
	obs, err := observeHalt(c)
	if err != nil {
		return err
	}
	st, err := loadHaltState(db, chainID)
	if err != nil {
		log.Printf("[halt][%s] %v", chainID, err)
		return nil
	}

	var pace time.Duration
	if obs.scheduled() {
		if pace, err = blockPace(c, obs); err != nil {
			log.Printf("[halt][%s] block pace unavailable: %v", chainID, err)
			pace = 0
		}
	}

	n := decideHalt(obs, st, pace, loadHaltThresholds(db))
	if n == nil {
		return nil
	}
	if n.Kind == haltNoticeResumed {
		n = confirmResumed(c, n)
	}
	msg := formatHaltPlain(n)
	// Record before sending: a failed insert must not turn into the same
	// notice sent on every poll.
	if err := recordHaltNotice(db, chainID, n, msg, now); err != nil {
		log.Printf("[halt][%s] %v; notice not sent", chainID, err)
		return nil
	}
	log.Printf("[halt][%s] %s", chainID, strings.ReplaceAll(msg, "\n", " | "))
	notifyHalt(db, chainID, n)
	return nil
}

// WatchChainHalt polls chainID's halt params until ctx is done, announcing a
// halt scheduled by GovDAO, its moves and cancellation, the reminders before
// it, and the chain resuming after it.
func WatchChainHalt(ctx context.Context, db *gorm.DB, chainID string) {
	c, err := haltClientForChain(chainID)
	if err != nil {
		log.Printf("[halt][%s] %v; halt watcher not started", chainID, err)
		return
	}
	log.Printf("[halt][%s] halt watcher started (every %v)", chainID, haltPollInterval)
	ticker := time.NewTicker(haltPollInterval)
	defer ticker.Stop()
	// A chain without the node params (an older gno) fails every poll the
	// same way: log a read error when it changes, not every 30 seconds.
	lastReadErr := ""
	for {
		// Checked before each poll: after a chain restart from the admin API,
		// the old watcher must not decide one more notice next to its
		// replacement.
		if ctx.Err() != nil {
			return
		}
		if err := checkChainHalt(db, chainID, c, time.Now()); err != nil {
			if err.Error() != lastReadErr {
				log.Printf("[halt][%s] read failed: %v", chainID, err)
				lastReadErr = err.Error()
			}
		} else if lastReadErr != "" {
			log.Printf("[halt][%s] read recovered", chainID)
			lastReadErr = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
