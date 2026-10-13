package jobclock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUntilNext(t *testing.T) {
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) // a 5-minute boundary
	cases := []struct {
		name   string
		now    time.Time
		period time.Duration
		offset time.Duration
		want   time.Duration
	}{
		{"before offset in cycle", base, Cycle, time.Minute, time.Minute},
		{"just after offset waits a full cycle", base.Add(time.Minute + time.Nanosecond), Cycle, time.Minute, Cycle - time.Nanosecond},
		{"exactly on offset waits a full cycle", base.Add(time.Minute), Cycle, time.Minute, Cycle},
		{"offset zero", base.Add(4 * time.Minute), Cycle, 0, time.Minute},
		{"offset larger than period wraps", base, Cycle, Cycle + 30*time.Second, 30 * time.Second},
		{"non-UTC location is the same instant", base.In(time.FixedZone("x", 3600+1800)), Cycle, time.Minute, time.Minute},
		{"short period", base.Add(10 * time.Second), 30 * time.Second, 5 * time.Second, 25 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, UntilNext(c.now, c.period, c.offset))
		})
	}
}

func TestUntilNext_NonPositivePeriod(t *testing.T) {
	require.Equal(t, time.Duration(0), UntilNext(time.Now(), 0, time.Minute))
}

func TestChainIndex(t *testing.T) {
	chains := []string{"betanet", "gnoland1", "test12"}
	require.Equal(t, 0, ChainIndex("betanet", chains))
	require.Equal(t, 2, ChainIndex("test12", chains))
	require.Equal(t, 0, ChainIndex("unknown", chains))
}

// minuteOf returns the minute of the 5-minute cycle an offset falls in.
func minuteOf(offset time.Duration) int {
	return int((offset % Cycle) / time.Minute)
}

// TestPhasePlan_HeavyJobsDoNotShareAMinute checks the plan for one to eight
// chains: no two chains share an offset, and neither the metrics burst nor any
// chain's latency check lands in the minute of the GovDAO watcher or of the
// valset refresh, nor in each other's.
func TestPhasePlan_HeavyJobsDoNotShareAMinute(t *testing.T) {
	for n := 1; n <= 8; n++ {
		chains := make([]string, n)
		for i := range chains {
			chains[i] = string(rune('a' + i))
		}
		checkPhasePlan(t, chains)
	}
}

func checkPhasePlan(t *testing.T, chains []string) {
	t.Helper()
	valset := map[time.Duration]bool{}
	latency := map[time.Duration]bool{}
	for _, c := range chains {
		v, l := ValsetOffset(c, chains), LatencyOffset(c, chains)
		require.False(t, valset[v], "two chains share the valset offset %v", v)
		require.False(t, latency[l], "two chains share the latency offset %v", l)
		valset[v], latency[l] = true, true
	}
	light := map[int]bool{minuteOf(GovDAOOffset()): true}
	for _, c := range chains {
		light[minuteOf(ValsetOffset(c, chains))] = true
	}
	metrics := minuteOf(MetricsOffset())
	require.False(t, light[metrics], "metrics shares a minute with GovDAO/valset")
	for _, c := range chains {
		m := minuteOf(LatencyOffset(c, chains))
		require.False(t, light[m], "latency %s shares a minute with GovDAO/valset", c)
		require.NotEqual(t, metrics, m, "latency %s shares a minute with metrics", c)
	}
}

func TestAlertOffset_SpreadsChains(t *testing.T) {
	chains := []string{"a", "b", "c"}
	interval := 30 * time.Second
	seen := map[time.Duration]bool{}
	for _, c := range chains {
		o := AlertOffset(c, chains, interval)
		require.Less(t, o, interval)
		require.False(t, seen[o], "two chains share the alert offset %v", o)
		seen[o] = true
	}
}
