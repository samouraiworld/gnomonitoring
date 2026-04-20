package gnovalidator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/chainmanager"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

type ValidatorInfo struct {
	Address     string
	VotingPower int64
	KeepRunning bool   // default true if realm not found
	ServerType  string // "cloud", "on-prem", "data-center", or ""
}

type ValsetChange struct {
	BlockNum int64
	Address  string
	NewPower int64
}

type ChainHealthSnapshot struct {
	// From RPC (zero values if RPCReachable == false)
	LatestBlockHeight int64
	LatestBlockTime   time.Time
	ConsensusRound    int
	RPCReachable      bool
	PeerCount         int
	MempoolTxCount    int
	MempoolTotalBytes int64

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

	ValidatorSet     []ValidatorInfo
	ValsetChanges    []ValsetChange
	PrecommitBitmap  map[string]bool // validator address → is precommitting in current round
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

		// Goroutine 3: Validators RPC → ValidatorSet
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := rpcClient.Validators(nil)
			if err != nil || result == nil {
				log.Printf("[health][%s] Validators() error: %v", chainID, err)
				return
			}
			infos := make([]ValidatorInfo, 0, len(result.Validators))
			for _, v := range result.Validators {
				if v == nil {
					continue
				}
				infos = append(infos, ValidatorInfo{
					Address:     v.Address.String(),
					VotingPower: v.VotingPower,
				})
			}
			mu.Lock()
			snap.ValidatorSet = infos
			mu.Unlock()
		}()

		// Goroutine 4: ABCIQuery vm/qrender r/sys/validators/v2 → ValsetChanges
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := rpcClient.ABCIQuery("vm/qrender", []byte("gno.land/r/sys/validators/v2:"))
			if err != nil {
				log.Printf("[health][%s] ABCIQuery valset changes error: %v", chainID, err)
				return
			}
			if resp == nil || resp.Response.Error != nil {
				log.Printf("[health][%s] ABCIQuery valset changes response error: %v", chainID, func() interface{} {
					if resp != nil {
						return resp.Response.Error
					}
					return "nil response"
				}())
				return
			}
			changes := parseValsetChanges(string(resp.Response.Data))
			mu.Lock()
			snap.ValsetChanges = changes
			mu.Unlock()
		}()

		// Goroutine 5: NetInfo → PeerCount
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := rpcClient.NetInfo()
			if err != nil || result == nil {
				log.Printf("[health][%s] NetInfo() error: %v", chainID, err)
				return
			}
			mu.Lock()
			snap.PeerCount = result.NPeers
			mu.Unlock()
		}()

		// Goroutine 6: NumUnconfirmedTxs → MempoolTxCount, MempoolTotalBytes
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := rpcClient.NumUnconfirmedTxs()
			if err != nil || result == nil {
				log.Printf("[health][%s] NumUnconfirmedTxs() error: %v", chainID, err)
				return
			}
			mu.Lock()
			snap.MempoolTxCount = result.Count
			snap.MempoolTotalBytes = result.TotalBytes
			mu.Unlock()
		}()

		wg.Wait()

		if rpcFailed {
			snap.RPCReachable = false
		}

		// Phase 2: DumpConsensusState — requires ValidatorSet from Phase 1.
		// Run synchronously after the WaitGroup so no mutex is needed.
		if len(snap.ValidatorSet) > 0 {
			snap.PrecommitBitmap = fetchPrecommitBitmap(rpcClient, snap.ValidatorSet)
		}

		// Phase 3: Enrich ValidatorSet with valopers realm data (KeepRunning, ServerType).
		if len(snap.ValidatorSet) > 0 {
			enrichValidatorInfoFromValopers(rpcClient, snap.ValidatorSet)
		}

	}

	rates, minBlock, maxBlock, err := CalculateValidatorStatusLast24h(db, chainID)
	if err != nil {
		log.Printf("[health][%s] CalculateValidatorStatusLast24h error: %v", chainID, err)
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

// peerStateExposed mirrors the JSON structure emitted by consensus.PeerStateExposed.
type peerStateExposed struct {
	RoundState struct {
		Precommits *bitArrayJSON `json:"precommits"`
	} `json:"round_state"`
}

// bitArrayJSON mirrors bitarray.BitArray for JSON decoding.
type bitArrayJSON struct {
	Bits  int      `json:"bits"`
	Elems []uint64 `json:"elems"`
}

// bitsSet returns the set of bit indices that are 1 in the BitArray.
func (b *bitArrayJSON) bitsSet() map[int]bool {
	out := make(map[int]bool)
	if b == nil {
		return out
	}
	for i, elem := range b.Elems {
		for j := 0; j < 64; j++ {
			globalIdx := i*64 + j
			if globalIdx >= b.Bits {
				return out
			}
			if elem&(uint64(1)<<uint(j)) != 0 {
				out[globalIdx] = true
			}
		}
	}
	return out
}

// orInto ORs the bits from src into dst (growing dst as needed).
func orInto(dst []uint64, src []uint64) []uint64 {
	if len(src) > len(dst) {
		dst = append(dst, make([]uint64, len(src)-len(dst))...)
	}
	for i, v := range src {
		dst[i] |= v
	}
	return dst
}

// fetchPrecommitBitmap calls DumpConsensusState and aggregates the precommit
// BitArrays from all peers into a map[validatorAddress]bool.
// Returns nil on any non-fatal failure.
func fetchPrecommitBitmap(rpcClient *FallbackRPCClient, valSet []ValidatorInfo) map[string]bool {
	result, err := rpcClient.DumpConsensusState()
	if err != nil || result == nil {
		log.Printf("[health] DumpConsensusState error: %v", err)
		return nil
	}
	if len(result.Peers) == 0 {
		return nil
	}

	// Aggregate precommit bitmasks across all peers via bitwise OR.
	var aggregatedElems []uint64
	aggregatedBits := 0

	for _, peer := range result.Peers {
		if peer.PeerState == nil {
			continue
		}
		var ps peerStateExposed
		raw := []byte(peer.PeerState)
		// Amino encodes PeerStateExposed as a JSON-quoted base64 string.
		// Step 1: unwrap the outer JSON string layer → get the base64 bytes.
		if len(raw) > 0 && raw[0] == '"' {
			var inner string
			if err := json.Unmarshal(raw, &inner); err != nil {
				log.Printf("[health] DumpConsensusState: failed to unwrap peer_state string: %v", err)
				continue
			}
			raw = []byte(inner)
		}
		// Step 2: if not a JSON object, try base64 decoding.
		if len(raw) > 0 && raw[0] != '{' {
			decoded, err := base64.StdEncoding.DecodeString(string(raw))
			if err != nil {
				decoded, err = base64.RawStdEncoding.DecodeString(string(raw))
				if err != nil {
					log.Printf("[health] DumpConsensusState: failed to base64 decode peer_state: %v", err)
					continue
				}
			}
			raw = decoded
		}
		if err := json.Unmarshal(raw, &ps); err != nil {
			log.Printf("[health] DumpConsensusState: failed to decode peer_state: %v", err)
			continue
		}
		pc := ps.RoundState.Precommits
		if pc == nil || pc.Bits == 0 {
			continue
		}
		if pc.Bits > aggregatedBits {
			aggregatedBits = pc.Bits
		}
		aggregatedElems = orInto(aggregatedElems, pc.Elems)
	}

	if aggregatedBits == 0 {
		return nil
	}

	aggregated := &bitArrayJSON{Bits: aggregatedBits, Elems: aggregatedElems}
	setBits := aggregated.bitsSet()

	bitmap := make(map[string]bool, len(valSet))
	for i, vi := range valSet {
		bitmap[vi.Address] = setBits[i]
	}
	return bitmap
}

// enrichValidatorInfoFromValopers queries the valopers realm for each validator in-place.
// For each validator, it calls ABCIQuery with path vm/qrender and data
// "gno.land/r/gnops/valopers:<address>", then parses the render output to extract
// ServerType. KeepRunning is always set to true because the realm does not expose it
// in the rendered output. All queries run in parallel under a 5-second global timeout.
// On any error the validator retains the safe defaults (KeepRunning=true, ServerType="").
func enrichValidatorInfoFromValopers(rpcClient *FallbackRPCClient, vals []ValidatorInfo) {
	var wg sync.WaitGroup
	for i := range vals {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			vals[idx].KeepRunning = true

			query := []byte("gno.land/r/gnops/valopers:" + vals[idx].Address)
			resp, err := rpcClient.ABCIQuery("vm/qrender", query)
			if err != nil || resp == nil || resp.Response.Error != nil {
				return
			}
			vals[idx].ServerType = parseValoperServerType(string(resp.Response.Data))
		}(i)
	}
	wg.Wait()
}

// parseValoperServerType extracts the "Server Type" field from the valopers realm
// render output for a single validator address. The expected format is:
//
//	Valoper's details:
//	## <Moniker>
//	<Description>
//
//	- Address: <addr>
//	- PubKey: <pubkey>
//	- Server Type: <serverType>
//
//	[Profile link](...)
//
// Returns "" if the field is not found.
func parseValoperServerType(render string) string {
	const prefix = "- Server Type: "
	for _, line := range strings.Split(render, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return ""
}

// parseValsetChanges parses the bullet-list emitted by r/sys/validators/v2 Render.
// Each entry has the format: "- #<blockNum>: <address> (<power>)"
// Malformed lines are skipped silently.
func parseValsetChanges(markdown string) []ValsetChange {
	var changes []ValsetChange
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- #") {
			continue
		}
		// Strip leading "- #"
		line = strings.TrimPrefix(line, "- #")
		// Split on ": " to get blockNum and the rest
		colonIdx := strings.Index(line, ": ")
		if colonIdx < 0 {
			continue
		}
		blockNum, err := strconv.ParseInt(line[:colonIdx], 10, 64)
		if err != nil {
			continue
		}
		rest := line[colonIdx+2:] // "<address> (<power>)"
		// Extract power from trailing " (<power>)"
		parenOpen := strings.LastIndex(rest, " (")
		parenClose := strings.LastIndex(rest, ")")
		if parenOpen < 0 || parenClose <= parenOpen {
			continue
		}
		addrStr := strings.TrimSpace(rest[:parenOpen])
		powerStr := rest[parenOpen+2 : parenClose]
		power, err := strconv.ParseInt(powerStr, 10, 64)
		if err != nil {
			continue
		}
		if addrStr == "" {
			continue
		}
		changes = append(changes, ValsetChange{
			BlockNum: blockNum,
			Address:  addrStr,
			NewPower: power,
		})
	}
	return changes
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

func CalculateValidatorStatusLast24h(db *gorm.DB, chainID string) (map[string]ValidatorRate, int64, int64, error) {
	type row struct {
		Addr              string
		Moniker           string
		TotalBlocks       int
		ParticipatedCount int
		FirstBlock        int64
		LastBlock         int64
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
		  AND date >= datetime('now', '-24 hours')
		GROUP BY addr
	`, chainID).Scan(&rows).Error
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


const reportSeparator = "---"

func uptimeRateEmoji(rate float64) string {
	switch {
	case rate >= 95.0:
		return "🟢"
	case rate >= 80.0:
		return "🟡"
	default:
		return "🔴"
	}
}

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

	// Network line.
	if snap.PeerCount > 0 || snap.MempoolTxCount > 0 {
		sb.WriteString(fmt.Sprintf("Network: %d peers | Mempool: %d pending txs\n", snap.PeerCount, snap.MempoolTxCount))
	}

	// Last known validator liveness section.
	if len(snap.ValidatorSet) > 0 {
		monikerMap := GetMonikerMap(chainID)

		var totalPower int64
		for _, vi := range snap.ValidatorSet {
			totalPower += vi.VotingPower
		}

		sorted := make([]ValidatorInfo, len(snap.ValidatorSet))
		copy(sorted, snap.ValidatorSet)
		sort.Slice(sorted, func(i, j int) bool {
			pi := snap.PrecommitBitmap[sorted[i].Address]
			pj := snap.PrecommitBitmap[sorted[j].Address]
			if pi != pj {
				return pi
			}
			return sorted[i].Address < sorted[j].Address
		})
		sb.WriteString(fmt.Sprintf("Last known validator liveness (%d active):\n", len(sorted)))
		{
			hasOffline := false
			for _, vi := range sorted {
				if !snap.PrecommitBitmap[vi.Address] {
					hasOffline = true
					break
				}
			}
			if len(sorted) > 5 || hasOffline {
				sb.WriteString("  Top 5 worst performers (last 24h):\n")
			}
		}
		for _, vi := range sorted {
			precommit := snap.PrecommitBitmap[vi.Address]
			precommitMark := "✗"
			statusEmoji := "🔴"
			if precommit {
				precommitMark = "✓"
				statusEmoji = "🟢"
			}
			moniker := monikerMap[vi.Address]
			if moniker == "" {
				moniker = vi.Address
				if len(moniker) > 10 {
					moniker = moniker[:10] + "..."
				}
			}
			addrShort := vi.Address
			if len(addrShort) > 10 {
				addrShort = addrShort[:10] + "..."
			}
			var line string
			if totalPower > 0 {
				powerPct := float64(vi.VotingPower) / float64(totalPower) * 100
				line = fmt.Sprintf("  %s %-14s (%s) precommit %s | %.1f%% power",
					statusEmoji, moniker, addrShort, precommitMark, powerPct)
			} else {
				line = fmt.Sprintf("  %s %-14s (%s) precommit %s",
					statusEmoji, moniker, addrShort, precommitMark)
			}
			if !vi.KeepRunning {
				line += " ⚠️ intends to leave"
			}
			sb.WriteString(line + "\n")
		}
	}

	// Valset changes section.
	if len(snap.ValsetChanges) > 0 {
		sb.WriteString(fmt.Sprintf("Valset changes (last %d):\n", len(snap.ValsetChanges)))
		for _, vc := range snap.ValsetChanges {
			if vc.NewPower == 0 {
				sb.WriteString(fmt.Sprintf("  Block #%d — %s removed\n", vc.BlockNum, vc.Address))
			} else {
				sb.WriteString(fmt.Sprintf("  Block #%d — %s added (power: %d)\n", vc.BlockNum, vc.Address, vc.NewPower))
			}
		}
	}

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
	}

	// Network line.
	if snap.PeerCount > 0 || snap.MempoolTxCount > 0 {
		sb.WriteString(fmt.Sprintf("Network: %d peers | Mempool: %d pending txs\n", snap.PeerCount, snap.MempoolTxCount))
	}

	// Validator set section — participation rates from rates parameter (yesterday's data).
	if len(rates) > 0 {
		monikerMap := GetMonikerMap(chainID)

		// Build a lookup from ValidatorSet for power and KeepRunning.
		type valMeta struct {
			VotingPower int64
			KeepRunning bool
			hasMeta     bool
		}
		valMetaMap := make(map[string]valMeta, len(snap.ValidatorSet))
		var totalPower int64
		for _, vi := range snap.ValidatorSet {
			valMetaMap[vi.Address] = valMeta{
				VotingPower: vi.VotingPower,
				KeepRunning: vi.KeepRunning,
				hasMeta:     true,
			}
			totalPower += vi.VotingPower
		}

		type rateEntry struct {
			addr    string
			rate    float64
			moniker string
			meta    valMeta
		}
		entries := make([]rateEntry, 0, len(rates))
		for addr, vr := range rates {
			moniker := monikerMap[addr]
			if moniker == "" {
				moniker = vr.Moniker
			}
			if moniker == "" {
				moniker = addr
				if len(moniker) > 10 {
					moniker = moniker[:10] + "..."
				}
			}
			entries = append(entries, rateEntry{
				addr:    addr,
				rate:    vr.Rate,
				moniker: moniker,
				meta:    valMetaMap[addr],
			})
		}
		// Sort ascending by rate (worst first), then address for stability.
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].rate != entries[j].rate {
				return entries[i].rate < entries[j].rate
			}
			return entries[i].addr < entries[j].addr
		})

		headerPower := ""
		if totalPower > 0 {
			headerPower = fmt.Sprintf(" — total power: %d", totalPower)
		}
		sb.WriteString(fmt.Sprintf("Validator set (%d active%s):\n", len(entries), headerPower))

		// Check if all validators are at 100%.
		allPerfect := true
		for _, e := range entries {
			if e.rate < 100.0 {
				allPerfect = false
				break
			}
		}
		if allPerfect {
			sb.WriteString(fmt.Sprintf("  All %d validators at 100%% uptime\n", len(entries)))
		} else {
			top := entries
			var rest []rateEntry
			if len(entries) > 5 {
				top = entries[:5]
				rest = entries[5:]
			}
			sb.WriteString("  Top 5 worst performers (last 24h):\n")
			for _, e := range top {
				uptimeEmoji := uptimeRateEmoji(e.rate)
				addrShort := e.addr
				if len(addrShort) > 10 {
					addrShort = addrShort[:10] + "..."
				}
				var line string
				if totalPower > 0 && e.meta.hasMeta {
					powerPct := float64(e.meta.VotingPower) / float64(totalPower) * 100
					line = fmt.Sprintf("  %s %-14s (%s) %.1f%% uptime | %.1f%% power",
						uptimeEmoji, e.moniker, addrShort, e.rate, powerPct)
				} else {
					line = fmt.Sprintf("  %s %-14s (%s) %.1f%% uptime",
						uptimeEmoji, e.moniker, addrShort, e.rate)
				}
				if e.meta.hasMeta && !e.meta.KeepRunning {
					line += " ⚠️ intends to leave"
				}
				sb.WriteString(line + "\n")
			}
			if len(rest) > 0 {
				allRestPerfect := true
				bestRest := 0.0
				for _, e := range rest {
					if e.rate < 100.0 {
						allRestPerfect = false
					}
					if e.rate > bestRest {
						bestRest = e.rate
					}
				}
				if allRestPerfect {
					sb.WriteString(fmt.Sprintf("  (%d others at 100%%)\n", len(rest)))
				} else {
					sb.WriteString(fmt.Sprintf("  (%d others, best: %.1f%%)\n", len(rest), bestRest))
				}
			}
		}
	}

	// Valset changes section.
	if len(snap.ValsetChanges) > 0 {
		sb.WriteString(fmt.Sprintf("Valset changes (last %d):\n", len(snap.ValsetChanges)))
		for _, vc := range snap.ValsetChanges {
			if vc.NewPower == 0 {
				sb.WriteString(fmt.Sprintf("  Block #%d — %s removed\n", vc.BlockNum, vc.Address))
			} else {
				sb.WriteString(fmt.Sprintf("  Block #%d — %s added (power: %d)\n", vc.BlockNum, vc.Address, vc.NewPower))
			}
		}
	}

	sb.WriteString(FormatMissedBlocksLast24h(snap.MissedLast24h))
	return sb.String()
}
