package gnovalidator

import (
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/chainmanager"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

type ChainHealthSnapshot struct {
	// From RPC (zero values if RPCReachable == false)
	LatestBlockHeight int64
	LatestBlockTime   time.Time
	ConsensusRound    int
	RPCReachable      bool

	// From in-memory flags
	IsStuck    bool
	IsDisabled bool

	// From DB (last RecentBlocksWindow blocks).
	// Used for the healthy-chain daily report (yesterday's participation).
	ValidatorRates map[string]ValidatorRate
	MinBlock       int64
	MaxBlock       int64

	// Missed blocks per validator in the last 24 hours.
	// Populated by FetchChainHealthSnapshot for use in daily reports.
	MissedLast24h []database.MissedBlockCount
}

func FetchChainHealthSnapshot(db *gorm.DB, chainID string) ChainHealthSnapshot {
	snap := ChainHealthSnapshot{
		IsStuck:    IsAlertSent(chainID, "all"),
		IsDisabled: !chainmanager.IsActive(chainID),
	}

	rpcClient, ok := GetChainRPCClient(chainID)
	if !ok || rpcClient == nil {
		snap.RPCReachable = false
	} else {
		snap.RPCReachable = true

		var mu sync.Mutex
		var wg sync.WaitGroup
		rpcFailed := false

		// Goroutine 1: Status → LatestBlockHeight, LatestBlockTime
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := rpcClient.Status()
			if err != nil || result == nil {
				log.Printf("[health][%s] Status() error: %v", chainID, err)
				mu.Lock()
				rpcFailed = true
				mu.Unlock()
				return
			}
			mu.Lock()
			snap.LatestBlockHeight = result.SyncInfo.LatestBlockHeight
			snap.LatestBlockTime = result.SyncInfo.LatestBlockTime
			// Derive IsStuck from block age as a reliable fallback independent of
			// the in-memory alertSent flag (which resets on restart).
			if !snap.IsStuck && !snap.LatestBlockTime.IsZero() &&
				time.Since(snap.LatestBlockTime) > 30*time.Minute {
				snap.IsStuck = true
			}
			mu.Unlock()
		}()

		// Goroutine 2: ConsensusState → ConsensusRound
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := rpcClient.ConsensusState()
			if err != nil || result == nil {
				log.Printf("[health][%s] ConsensusState() error: %v", chainID, err)
				mu.Lock()
				rpcFailed = true
				mu.Unlock()
				return
			}
			// HeightRoundStep format: "height/round/step"
			round := parseConsensusRound(result.RoundState.HeightRoundStep)
			mu.Lock()
			snap.ConsensusRound = round
			mu.Unlock()
		}()

		wg.Wait()

		if rpcFailed {
			snap.RPCReachable = false
		}

	}

	rates, minBlock, maxBlock, err := CalculateRecentValidatorStatus(db, chainID, GetThresholds().RecentBlocksWindow)
	if err != nil {
		log.Printf("[health][%s] CalculateRecentValidatorStatus error: %v", chainID, err)
	}
	snap.ValidatorRates = rates
	snap.MinBlock = minBlock
	snap.MaxBlock = maxBlock

	missed, err := database.GetMissedBlocksLast24h(db, chainID)
	if err != nil {
		log.Printf("[health][%s] GetMissedBlocksLast24h error: %v", chainID, err)
		// non-fatal: leave MissedLast24h nil
	} else {
		snap.MissedLast24h = missed
	}

	return snap
}

// parseConsensusRound parses the "height/round/step" string and returns the round number.
func parseConsensusRound(hrs string) int {
	parts := strings.Split(hrs, "/")
	if len(parts) < 2 {
		return 0
	}
	round, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return round
}

func CalculateRecentValidatorStatus(db *gorm.DB, chainID string, lastNBlocks int) (map[string]ValidatorRate, int64, int64, error) {
	type row struct {
		Addr             string
		Moniker          string
		TotalBlocks      int
		ParticipatedCount int
		FirstBlock       int64
		LastBlock        int64
	}
	var rows []row
	err := db.Raw(`
		SELECT addr, MAX(moniker) AS moniker,
			COUNT(*) AS total_blocks,
			SUM(CASE WHEN participated THEN 1 ELSE 0 END) AS participated_count,
			MIN(block_height) AS first_block,
			MAX(block_height) AS last_block
		FROM daily_participations
		WHERE chain_id = ?
		  AND block_height >= (SELECT MAX(block_height) FROM daily_participations WHERE chain_id = ?) - ?
		GROUP BY addr
	`, chainID, chainID, lastNBlocks).Scan(&rows).Error
	if err != nil {
		return nil, 0, 0, err
	}

	rates := make(map[string]ValidatorRate, len(rows))
	var minBlock, maxBlock int64
	first := true
	for _, r := range rows {
		if r.TotalBlocks > 0 {
			rate := float64(r.ParticipatedCount) / float64(r.TotalBlocks) * 100
			rates[r.Addr] = ValidatorRate{Rate: rate, Moniker: r.Moniker}
		}
		if first || r.FirstBlock < minBlock {
			minBlock = r.FirstBlock
		}
		if first || r.LastBlock > maxBlock {
			maxBlock = r.LastBlock
		}
		first = false
	}
	return rates, minBlock, maxBlock, nil
}

func formatBlockAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t).Truncate(time.Second)
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh ago", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm ago", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %ds ago", minutes, seconds)
	default:
		return fmt.Sprintf("%ds ago", seconds)
	}
}

func consensusLabel(round int) string {
	switch {
	case round <= 2:
		return "Normal"
	case round <= 10:
		return "Slightly slow"
	case round <= 50:
		return "Degraded (some validators lagging)"
	default:
		return "STUCK (no 2/3 majority)"
	}
}

func chainStatusEmoji(snap ChainHealthSnapshot) string {
	if snap.IsDisabled {
		return "⚫"
	}
	if snap.IsStuck || snap.ConsensusRound > 50 {
		return "🚨"
	}
	if snap.ConsensusRound > 10 {
		return "🟡"
	}
	return "🟢"
}

func validatorRateEmoji(rate float64) string {
	switch {
	case rate >= 95:
		return "🟢"
	case rate >= 70:
		return "🟡"
	case rate >= 50:
		return "🟠"
	default:
		return "🔴"
	}
}


func formatValidatorRates(rates map[string]ValidatorRate) string {
	if len(rates) == 0 {
		return "  (no data)\n"
	}
	var sb strings.Builder
	for addr, data := range rates {
		moniker := data.Moniker
		if moniker == "" {
			moniker = "unknown"
		}
		emoji := validatorRateEmoji(data.Rate)
		short := addr
		if len(addr) > 10 {
			short = addr[:10] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %s %-12s (%s) %.1f%%\n", emoji, moniker, short, data.Rate))
	}
	return sb.String()
}

const reportSeparator = "---"

func missedEmoji(missed int) string {
	t := GetThresholds()
	switch {
	case missed >= t.CriticalThreshold:
		return "🔴"
	case missed >= t.WarningThreshold:
		return "🟡"
	default:
		return "🟢"
	}
}

func FormatMissedBlocksLast24h(rows []database.MissedBlockCount) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	
	sb.WriteString("Missed blocks last 24h:\n")
	for _, r := range rows {
		moniker := r.Moniker
		if moniker == "" {
			moniker = "unknown"
		}
		addrShort := r.Addr
		if len(addrShort) > 10 {
			addrShort = addrShort[:10] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %s %-14s (%s) %d missed\n",
			missedEmoji(r.Missed), moniker, addrShort, r.Missed))
	}
	return sb.String()
}

// FormatMissedBlocksLast24hHTML is the HTML-safe variant for Telegram (parse_mode: HTML).
func FormatMissedBlocksLast24hHTML(rows []database.MissedBlockCount) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	
	sb.WriteString("Missed blocks last 24h:\n")
	for _, r := range rows {
		moniker := r.Moniker
		if moniker == "" {
			moniker = "unknown"
		}
		addrShort := r.Addr
		if len(addrShort) > 10 {
			addrShort = addrShort[:10] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %s <b>%-14s</b> (<code>%s</code>) %d missed\n",
			missedEmoji(r.Missed), html.EscapeString(moniker), html.EscapeString(addrShort), r.Missed))
	}
	return sb.String()
}

func FormatDisabledReport(chainID string, snap ChainHealthSnapshot) string {
	date := time.Now().UTC().Format("2006-01-02")
	var sb strings.Builder
	sb.WriteString(reportSeparator + "\n")
	sb.WriteString(fmt.Sprintf("📊 [%s] Daily Summary — %s\n", chainID, date))
	
	sb.WriteString("⚫ Monitoring OFF")
	if snap.LatestBlockHeight > 0 {
		sb.WriteString(fmt.Sprintf(" — Last known block: #%d at %s UTC",
			snap.LatestBlockHeight,
			snap.LatestBlockTime.UTC().Format("2006-01-02 15:04")))
	} else if snap.MaxBlock > 0 {
		sb.WriteString(fmt.Sprintf(" — Last known block in DB: #%d", snap.MaxBlock))
	}
	sb.WriteString("\n")
	return sb.String()
}

func FormatStuckReport(chainID string, snap ChainHealthSnapshot) string {
	date := time.Now().UTC().Format("2006-01-02")
	var sb strings.Builder
	sb.WriteString(reportSeparator + "\n")

	sb.WriteString(fmt.Sprintf("📊 [%s] Daily Summary — %s\n", chainID, date))
	
	emoji := chainStatusEmoji(snap)
	blockAge := formatBlockAge(snap.LatestBlockTime)
	stuckSince := ""
	if !snap.LatestBlockTime.IsZero() {
		stuckSince = fmt.Sprintf(" since %s UTC", snap.LatestBlockTime.UTC().Format("2006-01-02 15:04"))
	}
	sb.WriteString(fmt.Sprintf("%s Block #%d (%s) — Consensus: round %d — %s%s\n",
		emoji, snap.LatestBlockHeight, blockAge,
		snap.ConsensusRound, consensusLabel(snap.ConsensusRound), stuckSince))

	sb.WriteString(FormatMissedBlocksLast24h(snap.MissedLast24h))
	return sb.String()
}

func FormatHealthyReport(chainID, date string, snap ChainHealthSnapshot, rates map[string]ValidatorRate, minBlock, maxBlock int64) string {
	var sb strings.Builder
		sb.WriteString(reportSeparator + "\n")

	sb.WriteString(fmt.Sprintf("📊 [%s] Daily Summary — %s\n", chainID, date))

	if snap.RPCReachable {
		emoji := chainStatusEmoji(snap)
		blockAge := formatBlockAge(snap.LatestBlockTime)
		sb.WriteString(fmt.Sprintf("%s Block #%d (%s) — Consensus: round %d — %s\n",
			emoji, snap.LatestBlockHeight, blockAge,
			snap.ConsensusRound, consensusLabel(snap.ConsensusRound)))
		// sb.WriteString(reportSeparator + "\n")
	}

	// sb.WriteString(fmt.Sprintf("Participation yesterday (Blocks %d → %d):\n", minBlock, maxBlock))
	// sb.WriteString(formatValidatorRates(rates))
	sb.WriteString(FormatMissedBlocksLast24h(snap.MissedLast24h))
	return sb.String()
}
