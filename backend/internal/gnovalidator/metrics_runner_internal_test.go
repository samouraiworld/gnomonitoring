package gnovalidator

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLimitedRunner_CapsConcurrency runs the same number of units as one
// metrics update and checks no more than the limit ever run at once, every
// unit still runs, and a panicking unit does not take the others down.
func TestLimitedRunner_CapsConcurrency(t *testing.T) {
	const units = 13
	var wg sync.WaitGroup
	var panics atomic.Int32
	run := limitedRunner(&wg, metricsQueryConcurrency, func(any) { panics.Add(1) })

	var running, peak, done atomic.Int32
	for i := 0; i < units; i++ {
		run(func() {
			n := running.Add(1)
			defer running.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			done.Add(1)
			if i == 0 {
				panic("boom")
			}
		})
	}
	wg.Wait()

	require.Equal(t, int32(units), done.Load())
	require.Equal(t, int32(1), panics.Load())
	require.Equal(t, int32(metricsQueryConcurrency), peak.Load())
}
