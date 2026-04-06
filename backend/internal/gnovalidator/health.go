package gnovalidator

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/chainmanager"
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

	// Per-validator liveness from the last committed block's precommits.
	// true = validator signed; false = validator did not sign (MISSING).
	// nil map means RPC was unreachable or block data unavailable.
	ValidatorLiveness map[string]bool // addr -> signed

	// MonikerMap snapshot for display (addr -> moniker).
	// Populated from GetMonikerMap(chainID) alongside ValidatorLiveness.
	Monikers map[string]string

	// From DB (last RecentBlocksWindow blocks).
	// Used for the healthy-chain daily report (yesterday's participation).
	// On stuck/disabled chains, ValidatorLiveness is the primary signal.
	ValidatorRates map[string]ValidatorRate
	MinBlock       int64
	MaxBlock       int64
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

		// After the parallel goroutines complete, fetch the last committed block's
		// precommits to determine per-validator liveness. This is done sequentially
		// because it depends on snap.LatestBlockHeight set by goroutine 1.
		if snap.RPCReachable && snap.LatestBlockHeight > 0 {
			blockResult, err := rpcClient.Block(&snap.LatestBlockHeight)
			if err != nil || blockResult == nil || blockResult.Block == nil || blockResult.Block.LastCommit == nil {
				log.Printf("[health][%s] Block(%d) error: %v", chainID, snap.LatestBlockHeight, err)
			} else {
				// Build liveness map from precommits.
				liveness := make(map[string]bool)
				for _, precommit := range blockResult.Block.LastCommit.Precommits {
					if precommit != nil {
						liveness[precommit.ValidatorAddress.String()] = true
					}
				}

				// Fetch the validator set to enumerate all slots (including non-signers).
				valResult, err := rpcClient.Validators(&snap.LatestBlockHeight)
				if err != nil || valResult == nil {
					log.Printf("[health][%s] Validators(%d) error: %v", chainID, snap.LatestBlockHeight, err)
				} else {
					for _, v := range valResult.Validators {
						addr := v.Address.String()
						if _, ok := liveness[addr]; !ok {
							liveness[addr] = false
						}
					}
					snap.ValidatorLiveness = liveness
				}
			}

			// Build monikers: DB is primary source, in-memory overrides.
			monikers := make(map[string]string)
			var dbMonikers []struct {
				Addr    string `gorm:"column:addr"`
				Moniker string `gorm:"column:moniker"`
			}
			if err := db.Table("addr_monikers").
				Select("addr, moniker").
				Where("chain_id = ?", chainID).
				Find(&dbMonikers).Error; err == nil {
				for _, row := range dbMonikers {
					monikers[row.Addr] = row.Moniker
				}
			}
			for addr, moniker := range GetMonikerMap(chainID) {
				if moniker != "" {
					monikers[addr] = moniker
				}
			}
			snap.Monikers = monikers
		}
	}

	rates, minBlock, maxBlock, err := CalculateRecentValidatorStatus(db, chainID, GetThresholds().RecentBlocksWindow)
	if err != nil {
		log.Printf("[health][%s] CalculateRecentValidatorStatus error: %v", chainID, err)
	}
	snap.ValidatorRates = rates
	snap.MinBlock = minBlock
	snap.MaxBlock = maxBlock

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

// formatValidatorLiveness formats per-validator liveness from the last committed
// block's precommits. monikers maps addr -> display name; it may be nil or empty.
// Signed validators are listed first, then missing ones, each group sorted by display name.
func formatValidatorLiveness(liveness map[string]bool, monikers map[string]string) string {
	if len(liveness) == 0 {
		return "  (no data)\n"
	}

	type entry struct {
		addr    string
		name    string // display name (moniker or truncated addr)
		signed  bool
	}
	entries := make([]entry, 0, len(liveness))
	for addr, signed := range liveness {
		name := monikers[addr]
		if name == "" {
			if len(addr) > 10 {
				name = addr[:10] + "..."
			} else {
				name = addr
			}
		}
		entries = append(entries, entry{addr: addr, name: name, signed: signed})
	}
	// Sort: signed validators first, then alphabetically by display name.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].signed != entries[j].signed {
			return entries[i].signed
		}
		return entries[i].name < entries[j].name
	})

	var sb strings.Builder
	for _, e := range entries {
		short := e.addr
		if len(short) > 10 {
			short = short[:10] + "..."
		}
		if e.signed {
			sb.WriteString(fmt.Sprintf("  🟢 %-12s (%s)\n", e.name, short))
		} else {
			sb.WriteString(fmt.Sprintf("  🔴 %-12s (%s)  MISSING\n", e.name, short))
		}
	}
	return sb.String()
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

func FormatDisabledReport(chainID string, snap ChainHealthSnapshot) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⚫ [%s] Chain status — MONITORING OFF\n", chainID))
	if snap.LatestBlockHeight > 0 {
		sb.WriteString(fmt.Sprintf("Last known block: #%d at %s UTC\n",
			snap.LatestBlockHeight,
			snap.LatestBlockTime.UTC().Format("2006-01-02 15:04"),
		))
	} else if snap.MaxBlock > 0 {
		sb.WriteString(fmt.Sprintf("Last known block in DB: #%d\n", snap.MaxBlock))
	}
	if snap.ValidatorLiveness != nil {
		sb.WriteString(fmt.Sprintf("\nValidator status at last block #%d:\n", snap.LatestBlockHeight))
		sb.WriteString(formatValidatorLiveness(snap.ValidatorLiveness, snap.Monikers))
	} else if len(snap.ValidatorRates) > 0 {
		sb.WriteString(fmt.Sprintf("\nValidator participation (last %d blocks — RPC unreachable):\n", GetThresholds().RecentBlocksWindow))
		sb.WriteString(formatValidatorRates(snap.ValidatorRates))
	} else {
		sb.WriteString("\n(RPC unreachable — no validator data available)\n")
	}
	return sb.String()
}

func FormatStuckReport(chainID string, snap ChainHealthSnapshot) string {
	var sb strings.Builder
	emoji := chainStatusEmoji(snap)
	blockAge := formatBlockAge(snap.LatestBlockTime)
	sb.WriteString(fmt.Sprintf("%s [%s] Chain status — block #%d (%s)\n",
		emoji, chainID, snap.LatestBlockHeight, blockAge))

	stuckSince := ""
	if !snap.LatestBlockTime.IsZero() {
		stuckSince = fmt.Sprintf(" since %s UTC", snap.LatestBlockTime.UTC().Format("2006-01-02 15:04"))
	}
	sb.WriteString(fmt.Sprintf("Consensus: round %d — %s%s\n",
		snap.ConsensusRound, consensusLabel(snap.ConsensusRound), stuckSince))

	if snap.ValidatorLiveness != nil {
		sb.WriteString(fmt.Sprintf("\nValidator status at last block #%d:\n", snap.LatestBlockHeight))
		sb.WriteString(formatValidatorLiveness(snap.ValidatorLiveness, snap.Monikers))
	} else if len(snap.ValidatorRates) > 0 {
		sb.WriteString(fmt.Sprintf("\nValidator participation (last %d blocks — RPC unreachable):\n", GetThresholds().RecentBlocksWindow))
		sb.WriteString(formatValidatorRates(snap.ValidatorRates))
	}
	return sb.String()
}

func FormatHealthyReport(chainID, date string, rates map[string]ValidatorRate, minBlock, maxBlock int64) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 [%s] Daily Summary — %s\n\n", chainID, date))
	sb.WriteString(fmt.Sprintf("Participation yesterday (Blocks %d → %d):\n", minBlock, maxBlock))
	sb.WriteString(formatValidatorRates(rates))
	return sb.String()
}
