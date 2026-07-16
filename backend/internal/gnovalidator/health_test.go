package gnovalidator

import (
	"strings"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

func TestFormatMissedBlocksLast24hHTML_ShowsScoreWhenKnown(t *testing.T) {
	rows := []database.MissedBlockCount{
		{Addr: "g1known", Moniker: "known-mon", Missed: 7},
		{Addr: "g1unknown", Moniker: "unknown-mon", Missed: 2},
	}
	scoreByAddr := map[string]int{"g1known": 42}

	got := FormatMissedBlocksLast24hHTML(rows, scoreByAddr)

	if !strings.Contains(got, "known-mon") || !strings.Contains(got, "Score: 42") {
		t.Fatalf("expected known-mon line to show Score: 42, got: %s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "unknown-mon") && strings.Contains(line, "Score:") {
			t.Fatalf("unknown-mon has no entry in scoreByAddr and must not show a Score suffix, got line: %s", line)
		}
	}
}

func TestFormatMissedBlocksLast24hHTML_EmptyRowsReturnsEmptyString(t *testing.T) {
	got := FormatMissedBlocksLast24hHTML(nil, map[string]int{"g1x": 10})
	if got != "" {
		t.Fatalf("expected empty string for no rows regardless of scoreByAddr, got: %q", got)
	}
}
