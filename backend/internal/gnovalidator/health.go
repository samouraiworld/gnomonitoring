package gnovalidator

import (
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
		if err := json.Unmarshal(peer.PeerState, &ps); err != nil {
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

// parseValsetChanges parses the markdown table returned by r/sys/validators/v2 render.
// Each data row has the format: | blockNum | address | power |
// Header and separator lines are skipped. Malformed rows are skipped silently.
func parseValsetChanges(markdown string) []ValsetChange {
	var changes []ValsetChange
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
			continue
		}
		// Strip leading/trailing pipes then split
		inner := strings.TrimPrefix(strings.TrimSuffix(line, "|"), "|")
		cols := strings.Split(inner, "|")
		if len(cols) != 3 {
			continue
		}
		blockStr := strings.TrimSpace(cols[0])
		addrStr := strings.TrimSpace(cols[1])
		powerStr := strings.TrimSpace(cols[2])

		// Skip header/separator rows: must be numeric block, non-empty address, numeric power
		blockNum, err := strconv.ParseInt(blockStr, 10, 64)
		if err != nil {
			continue
		}
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
		for _, vi := range sorted {
			precommit := snap.PrecommitBitmap[vi.Address]
			precommitMark := "✗"
			statusEmoji := "🔴"
			if precommit {
				precommitMark = "✓"
				statusEmoji = "🟢"
			}
			addrShort := vi.Address
			if len(addrShort) > 10 {
				addrShort = addrShort[:10] + "..."
			}
			line := fmt.Sprintf("  %s %-14s (%s) precommit %s | power: %d",
				statusEmoji, vi.Address, addrShort, precommitMark, vi.VotingPower)
			if !vi.KeepRunning {
				line += " ⚠️ intends to leave"
			}
			sb.WriteString(line + "\n")
		}
	}

	// Infrastructure line.
	if len(snap.ValidatorSet) > 0 {
		typeCounts := map[string]int{}
		hasType := false
		for _, vi := range snap.ValidatorSet {
			if vi.ServerType != "" {
				typeCounts[vi.ServerType]++
				hasType = true
			}
		}
		if hasType {
			var parts []string
			for _, t := range []string{"cloud", "on-prem", "data-center"} {
				if c, ok := typeCounts[t]; ok {
					parts = append(parts, fmt.Sprintf("%d %s", c, t))
					delete(typeCounts, t)
				}
			}
			for t, c := range typeCounts {
				parts = append(parts, fmt.Sprintf("%d %s", c, t))
			}
			sb.WriteString("Infrastructure: " + strings.Join(parts, ", ") + "\n")
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

	// Validator set section.
	if len(snap.ValidatorSet) > 0 {
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
		sb.WriteString(fmt.Sprintf("Validator set (%d active):\n", len(sorted)))
		for _, vi := range sorted {
			precommit := snap.PrecommitBitmap[vi.Address]
			precommitMark := "✗"
			statusEmoji := "🔴"
			if precommit {
				precommitMark = "✓"
				statusEmoji = "🟢"
			}
			addrShort := vi.Address
			if len(addrShort) > 10 {
				addrShort = addrShort[:10] + "..."
			}
			line := fmt.Sprintf("  %s %-14s (%s) precommit %s | power: %d",
				statusEmoji, vi.Address, addrShort, precommitMark, vi.VotingPower)
			if !vi.KeepRunning {
				line += " ⚠️ intends to leave"
			}
			sb.WriteString(line + "\n")
		}
	}

	// Infrastructure line.
	if len(snap.ValidatorSet) > 0 {
		typeCounts := map[string]int{}
		hasType := false
		for _, vi := range snap.ValidatorSet {
			if vi.ServerType != "" {
				typeCounts[vi.ServerType]++
				hasType = true
			}
		}
		if hasType {
			var parts []string
			for _, t := range []string{"cloud", "on-prem", "data-center"} {
				if c, ok := typeCounts[t]; ok {
					parts = append(parts, fmt.Sprintf("%d %s", c, t))
					delete(typeCounts, t)
				}
			}
			for t, c := range typeCounts {
				parts = append(parts, fmt.Sprintf("%d %s", c, t))
			}
			sb.WriteString("Infrastructure: " + strings.Join(parts, ", ") + "\n")
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
