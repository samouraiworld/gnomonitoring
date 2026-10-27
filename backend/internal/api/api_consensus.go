package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

const consensusCacheTTL = 30 * time.Second
const consensusQueryTimeout = 10 * time.Second
const maxConsensusCachedChains = 128

type latencyWindow struct {
	P50MS        *float64   `json:"p50_ms"`
	P90MS        *float64   `json:"p90_ms"`
	SampleCount  int64      `json:"sample_count"`
	LastSampleAt *time.Time `json:"last_sample_at"`
}

type validatorConsensus struct {
	Addr         string                   `json:"addr"`
	Moniker      string                   `json:"moniker"`
	PrecommitLag map[string]latencyWindow `json:"precommit_lag"`
}

type chainBFT struct {
	Available         bool       `json:"available"`
	Reason            string     `json:"reason,omitempty"`
	WindowBlocks      int        `json:"window_blocks"`
	FromHeight        int64      `json:"from_height"`
	ToHeight          int64      `json:"to_height"`
	CollectedHeights  int64      `json:"collected_heights"`
	LastBlockAt       *time.Time `json:"last_block_at"`
	MarginPercent     *float64   `json:"margin_percent"`
	OnlineVotingPower *int64     `json:"online_voting_power"`
	TotalVotingPower  *int64     `json:"total_voting_power"`
}

type consensusReport struct {
	ChainID         string               `json:"chain_id"`
	ComputedAt      time.Time            `json:"computed_at"`
	CacheTTLSeconds int                  `json:"cache_ttl_seconds"`
	BFT             chainBFT             `json:"bft"`
	Validators      []validatorConsensus `json:"validators"`
}

// Each handler owns its cache and DB: separately registered servers cannot
// share snapshots. Only validated configured chains become keys; address
// filters are applied after the cached chain load, so arbitrary addresses
// cannot grow the cache or each trigger a seven-day percentile scan.
type consensusReportHandler struct {
	db    *gorm.DB
	mu    sync.Mutex
	cache map[string]consensusReport
	loads singleflight.Group
}

func newConsensusReportHandler(db *gorm.DB) *consensusReportHandler {
	return &consensusReportHandler{db: db, cache: make(map[string]consensusReport)}
}

func (h *consensusReportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	EnableCORS(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chainID, err := GetChainIDFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addr := r.URL.Query().Get("addr")
	if len(addr) > 128 || strings.TrimSpace(addr) != addr || strings.ContainsFunc(addr, unicode.IsControl) {
		http.Error(w, "addr must be an exact validator address", http.StatusBadRequest)
		return
	}
	report, err := h.snapshot(r.Context(), chainID)
	if err != nil {
		log.Printf("[consensus-report][%s] %v", chainID, err)
		http.Error(w, "Consensus data temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if addr != "" {
		// A new slice keeps the cached roster immutable under concurrent reads.
		filtered := make([]validatorConsensus, 0, 1)
		for _, v := range report.Validators {
			if v.Addr == addr {
				filtered = append(filtered, v)
				break
			}
		}
		report.Validators = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	// Freshness is explicit in the payload. Do not let intermediary caches
	// keep a live quorum observation beyond this handler's bounded TTL.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(report)
}

func (h *consensusReportHandler) cached(chainID string) (consensusReport, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	report, ok := h.cache[chainID]
	return report, ok && time.Since(report.ComputedAt) < consensusCacheTTL
}

func (h *consensusReportHandler) snapshot(ctx context.Context, chainID string) (consensusReport, error) {
	if err := ctx.Err(); err != nil {
		return consensusReport{}, err
	}
	if report, ok := h.cached(chainID); ok {
		return report, nil
	}
	result := h.loads.DoChan(chainID, func() (any, error) {
		if report, ok := h.cached(chainID); ok {
			return report, nil
		}
		// One disconnected reader must not cancel the shared load for others.
		// The independent deadline still bounds all SQL work.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), consensusQueryTimeout)
		defer cancel()
		report, err := buildConsensusReport(h.db.WithContext(loadCtx), chainID)
		if err != nil {
			return nil, err
		}
		h.mu.Lock()
		// Configuration reloads may remove chains. Evict expired entries and
		// cap the cache independently of how many historical IDs were configured.
		for key, cached := range h.cache {
			if time.Since(cached.ComputedAt) >= consensusCacheTTL {
				delete(h.cache, key)
			}
		}
		if len(h.cache) >= maxConsensusCachedChains {
			var oldestKey string
			var oldest time.Time
			for key, cached := range h.cache {
				if oldest.IsZero() || cached.ComputedAt.Before(oldest) {
					oldestKey, oldest = key, cached.ComputedAt
				}
			}
			delete(h.cache, oldestKey)
		}
		h.cache[chainID] = report
		h.mu.Unlock()
		return report, nil
	})
	select {
	case <-ctx.Done():
		return consensusReport{}, ctx.Err()
	case loaded := <-result:
		if loaded.Err != nil {
			return consensusReport{}, loaded.Err
		}
		report, ok := loaded.Val.(consensusReport)
		if !ok {
			return consensusReport{}, fmt.Errorf("invalid consensus snapshot")
		}
		return report, nil
	}
}

func buildConsensusReport(db *gorm.DB, chainID string) (consensusReport, error) {
	ctx, err := database.LoadValidatorReportContext(db, chainID)
	if err != nil {
		return consensusReport{}, err
	}
	latencies, err := database.GetPrecommitLatencyMultiWindow(db, chainID)
	if err != nil {
		return consensusReport{}, err
	}
	byAddr := make(map[string]database.PrecommitLatencyMultiWindow, len(latencies))
	for _, row := range latencies {
		byAddr[row.Addr] = row
	}
	t := gnovalidator.GetThresholds()
	window, err := database.GetBFTWindow(db, chainID, t.BFTAlertWindowBlocks)
	if err != nil {
		return consensusReport{}, err
	}
	now := time.Now().UTC()
	margin, reason := gnovalidator.ObserveBFTWindow(window, t, now)
	report := consensusReport{
		ChainID: chainID, ComputedAt: now, CacheTTLSeconds: int(consensusCacheTTL.Seconds()),
		BFT: chainBFT{Available: margin.Available, Reason: reason, WindowBlocks: t.BFTAlertWindowBlocks,
			FromHeight: window.MinHeight, ToHeight: window.MaxHeight, CollectedHeights: window.Heights},
		Validators: make([]validatorConsensus, 0, len(ctx.Roster)),
	}
	if !window.LastBlockTime.IsZero() {
		last := window.LastBlockTime.UTC()
		report.BFT.LastBlockAt = &last
	}
	if margin.Available {
		pct := margin.MarginPercent()
		report.BFT.MarginPercent = &pct
		report.BFT.OnlineVotingPower = &margin.OnlineVP
		report.BFT.TotalVotingPower = &margin.TotalVP
	}
	roster := make(map[string]string, len(ctx.Roster)+len(ctx.VPByAddr))
	for _, identity := range ctx.Roster {
		roster[identity.Addr] = identity.Moniker
	}
	for addr := range ctx.VPByAddr {
		// Existing roster names already follow MonikerSQL's fallback rule.
		// Raw snapshot names may be empty/unknown; use them only for members
		// without participation history, as the health report does.
		if _, exists := roster[addr]; !exists {
			roster[addr] = ctx.VPMonikerByAddr[addr]
		}
	}
	addresses := make([]string, 0, len(roster))
	for addr := range roster {
		addresses = append(addresses, addr)
	}
	sort.Strings(addresses)
	for _, addr := range addresses {
		if ctx.ValsetFilterActive && ctx.VPByAddr[addr] <= 0 {
			continue
		}
		row := byAddr[addr]
		report.Validators = append(report.Validators, validatorConsensus{
			Addr: addr, Moniker: roster[addr],
			PrecommitLag: map[string]latencyWindow{
				"last_24h": {row.LagP50_24h, row.LagP90_24h, row.Samples24h, utcSampleTime(row.LastSample24h)},
				"last_7d":  {row.LagP50_7d, row.LagP90_7d, row.Samples7d, utcSampleTime(row.LastSample7d)},
			},
		})
	}
	return report, nil
}

func utcSampleTime(sample *time.Time) *time.Time {
	if sample == nil {
		return nil
	}
	utc := sample.UTC()
	return &utc
}
