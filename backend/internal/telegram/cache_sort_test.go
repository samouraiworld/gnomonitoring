package telegram

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

// TestListFormatters_DoNotMutateCachedSlice guards against the list
// formatters sorting the slice shared through metricsCache in place. Every
// chat reads the same cached slice, so an in-place sort is a data race between
// concurrent requests (caught by -race) and reorders the cache for everyone.
func TestListFormatters_DoNotMutateCachedSlice(t *testing.T) {
	const chainID = "cache-sort-test"
	const period = "current_month"

	cases := []struct {
		name   string
		key    string
		seed   any
		format func(sortOrder string) error
	}{
		{
			name: "participation",
			key:  chainID + ":status:" + period,
			seed: []database.ParticipationRate{
				{Addr: "g1b", Moniker: "b", ParticipationRate: 50},
				{Addr: "g1a", Moniker: "a", ParticipationRate: 10},
				{Addr: "g1c", Moniker: "c", ParticipationRate: 90},
				{Addr: "g1d", Moniker: "d", ParticipationRate: 30},
			},
			format: func(sortOrder string) error {
				_, _, _, err := formatParticipationRAte(nil, chainID, period, 1, 10, "", sortOrder)
				return err
			},
		},
		{
			name: "uptime",
			key:  chainID + ":uptime",
			seed: []database.UptimeMetrics{
				{Addr: "g1b", Moniker: "b", Uptime: 50},
				{Addr: "g1a", Moniker: "a", Uptime: 10},
				{Addr: "g1c", Moniker: "c", Uptime: 90},
				{Addr: "g1d", Moniker: "d", Uptime: 30},
			},
			format: func(sortOrder string) error {
				_, _, _, err := formatUptime(nil, chainID, 1, 10, "", sortOrder)
				return err
			},
		},
		{
			name: "operation_time",
			key:  chainID + ":operation_time",
			seed: []database.OperationTimeMetrics{
				{Addr: "g1b", Moniker: "b", DaysDiff: 5},
				{Addr: "g1a", Moniker: "a", DaysDiff: 1},
				{Addr: "g1c", Moniker: "c", DaysDiff: 9},
				{Addr: "g1d", Moniker: "d", DaysDiff: 3},
			},
			format: func(string) error {
				_, _, _, err := formatOperationTime(nil, chainID, 1, 10, "")
				return err
			},
		},
		{
			name: "tx_contrib",
			key:  chainID + ":tx_contrib:" + period,
			seed: []database.TxContribMetrics{
				{Addr: "g1b", Moniker: "b", TxContrib: 50},
				{Addr: "g1a", Moniker: "a", TxContrib: 10},
				{Addr: "g1c", Moniker: "c", TxContrib: 90},
				{Addr: "g1d", Moniker: "d", TxContrib: 30},
			},
			format: func(sortOrder string) error {
				_, _, _, err := FormatTxcontrib(nil, chainID, period, 1, 10, "", sortOrder)
				return err
			},
		},
		{
			name: "missing",
			key:  chainID + ":missing:" + period,
			seed: []database.MissingBlockMetrics{
				{Addr: "g1b", Moniker: "b", MissingBlock: 5},
				{Addr: "g1a", Moniker: "a", MissingBlock: 1},
				{Addr: "g1c", Moniker: "c", MissingBlock: 9},
				{Addr: "g1d", Moniker: "d", MissingBlock: 3},
			},
			format: func(string) error {
				_, _, _, err := formatMissing(nil, chainID, period, 1, 10, "")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// cloneSeed keeps an independent copy of the seeded order to
			// compare the cache against once the formatters have run.
			want := cloneSeed(t, tc.seed)
			setCached(tc.key, tc.seed, time.Minute)
			t.Cleanup(func() {
				metricsCacheMu.Lock()
				defer metricsCacheMu.Unlock()
				delete(metricsCache, tc.key)
			})

			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				sortOrder := "asc"
				if i%2 == 1 {
					sortOrder = "desc"
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					assert.NoError(t, tc.format(sortOrder))
				}()
			}
			wg.Wait()

			cached, ok := getCached(tc.key)
			require.True(t, ok)
			assert.Equal(t, want, cached, "formatter reordered the cached slice")
		})
	}
}

func cloneSeed(t *testing.T, seed any) any {
	t.Helper()
	switch s := seed.(type) {
	case []database.ParticipationRate:
		return append([]database.ParticipationRate(nil), s...)
	case []database.UptimeMetrics:
		return append([]database.UptimeMetrics(nil), s...)
	case []database.OperationTimeMetrics:
		return append([]database.OperationTimeMetrics(nil), s...)
	case []database.TxContribMetrics:
		return append([]database.TxContribMetrics(nil), s...)
	case []database.MissingBlockMetrics:
		return append([]database.MissingBlockMetrics(nil), s...)
	}
	t.Fatalf("unsupported seed type %T", seed)
	return nil
}
