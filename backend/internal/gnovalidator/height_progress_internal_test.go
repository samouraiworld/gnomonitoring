package gnovalidator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClassifyHeightObservation(t *testing.T) {
	cases := []struct {
		name     string
		latest   int64
		lastSeen int64
		want     heightObservation
	}{
		{"first observation", 100, 0, heightAdvanced},
		{"advanced", 101, 100, heightAdvanced},
		{"stalled", 100, 100, heightStalled},
		{"regressed by one", 99, 100, heightRegressed},
		{"regressed a lot", 40, 100, heightRegressed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyHeightObservation(tc.latest, tc.lastSeen))
		})
	}
}

// TestEvaluateRegression covers the persistence tracking that distinguishes a
// transient regression (endpoint fail-over flap, must keep being ignored no
// matter how many times it repeats) from a persistent one (the chain
// genuinely rewound and is now stalled at the lower height, must eventually
// be accepted so the stalled branch — and its CRITICAL alert — can run).
func TestEvaluateRegression(t *testing.T) {
	bound := 20 * time.Second
	t0 := time.Now()

	t.Run("a regression seen repeatedly within bound stays ignored", func(t *testing.T) {
		since := time.Time{}

		accept, next := evaluateRegression(since, t0, bound)
		assert.False(t, accept)
		assert.Equal(t, t0, next, "first regressed observation starts the tracker at 'now'")
		since = next

		accept, next = evaluateRegression(since, t0.Add(5*time.Second), bound)
		assert.False(t, accept)
		assert.Equal(t, t0, next, "regressedSince must not move while repeated observations stay within bound")
		since = next

		accept, next = evaluateRegression(since, t0.Add(bound), bound)
		assert.False(t, accept, "exactly at the bound is not yet past it")
		since = next

		accept, _ = evaluateRegression(since, t0.Add(bound-time.Nanosecond), bound)
		assert.False(t, accept)
	})

	t.Run("a regression is accepted once it exceeds bound", func(t *testing.T) {
		since := t0

		accept, next := evaluateRegression(since, t0.Add(bound+time.Second), bound)
		assert.True(t, accept, "a regression that persisted past the bound must be accepted as the new baseline")
		assert.True(t, next.IsZero(), "the tracker resets once the regression is accepted")
	})

	t.Run("a non-regressed observation in between resets the tracker", func(t *testing.T) {
		// A regression starts tracking at t0...
		since := t0

		// ...but before it can accumulate past bound, a non-regressed
		// observation arrives. CollectParticipation resets the tracker to
		// the zero Time in that case rather than calling evaluateRegression.
		since = time.Time{}

		// A fresh regression episode beginning well after t0+bound must NOT
		// be immediately accepted: it gets its own bound window and does not
		// inherit elapsed time from the earlier, unrelated episode.
		accept, next := evaluateRegression(since, t0.Add(bound+time.Second), bound)
		assert.False(t, accept, "a freshly reset tracker must not accept just because a lot of absolute time has passed since an earlier, distinct episode")
		assert.Equal(t, t0.Add(bound+time.Second), next, "the reset episode starts its own clock at the observation time")
	})
}
