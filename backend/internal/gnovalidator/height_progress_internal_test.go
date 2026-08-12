package gnovalidator

import (
	"testing"

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
