package gnovalidator

import (
	"strings"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
)

func fieldValue(t *testing.T, d internal.AlertData, name string) string {
	t.Helper()
	for _, f := range d.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	t.Fatalf("field %q missing from %+v", name, d.Fields)
	return ""
}

func TestBuildLatencyAlertData(t *testing.T) {
	ratio := 0.49
	ev := latencyEval{
		Stat: latencyStat{
			Addr: "g1slow", Moniker: "samourai-crew-validator-1", Samples: 1043,
			P50: 67, P90: 120, LateRatio: &ratio, MinHeight: 112000, MaxHeight: 113043,
		},
		PeerMedianP50: 8,
		Verdict:       verdictTrigger,
	}

	d := buildLatencyAlertData("gnoland1", ev, 60)

	if d.Level != internal.AlertLatency {
		t.Errorf("Level = %q, want LATENCY", d.Level)
	}
	if d.Title != "LATENCY — signing late for quorum" {
		t.Errorf("Title = %q", d.Title)
	}
	if len(d.Mentions) != 0 {
		t.Errorf("Mentions = %v, want none on a latency alert", d.Mentions)
	}
	if v := fieldValue(t, d, "validator"); v != "samourai-crew-validator-1 (g1slow)" {
		t.Errorf("validator = %q", v)
	}
	if v := fieldValue(t, d, "precommit lag (60m)"); v != "p50 67 ms / p90 120 ms (peers median p50: 8 ms)" {
		t.Errorf("lag = %q", v)
	}
	if v := fieldValue(t, d, "late for quorum"); v != "49% of 1043 signed blocks" {
		t.Errorf("late for quorum = %q", v)
	}
	if v := fieldValue(t, d, "blocks"); v != "112000 -> 113043" {
		t.Errorf("blocks = %q", v)
	}
	note := fieldValue(t, d, "note")
	for _, want := range []string{"No blocks missed", "flush_throttle_timeout", "peer_gossip_sleep_duration", "timeout_commit", "NTP"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not mention %q", note, want)
		}
	}
}

func TestBuildLatencyAlertData_NoQuorumSamples(t *testing.T) {
	ev := latencyEval{
		Stat:          latencyStat{Addr: "g1a", Moniker: "A", Samples: 400, P50: 80, P90: 90, MinHeight: 1, MaxHeight: 2},
		PeerMedianP50: 5,
	}

	d := buildLatencyAlertData("dev", ev, 60)

	for _, f := range d.Fields {
		if f.Name == "late for quorum" {
			t.Fatal("the late-for-quorum field must be omitted when the ratio is unknown")
		}
	}
}

func TestBuildLatencyResolvedData(t *testing.T) {
	ev := latencyEval{
		Stat:          latencyStat{Addr: "g1a", Moniker: "A", Samples: 900, P50: 9, P90: 12, MinHeight: 5, MaxHeight: 9},
		PeerMedianP50: 8,
	}

	d := buildLatencyResolvedData("gnoland1", ev, 60)

	if d.Level != internal.AlertResolved {
		t.Errorf("Level = %q, want RESOLVED", d.Level)
	}
	if d.Title != "LATENCY RESOLVED" {
		t.Errorf("Title = %q", d.Title)
	}
	if v := fieldValue(t, d, "precommit lag (60m)"); v != "p50 9 ms / p90 12 ms (peers median p50: 8 ms)" {
		t.Errorf("lag = %q", v)
	}
}
