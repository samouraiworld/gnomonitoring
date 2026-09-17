package gnovalidator

import (
	"fmt"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
)

// latencyNote is the explanatory line every latency alert carries. It names
// the three consensus settings behind every case seen so far, and states
// plainly that no block was missed so the alert is not mistaken for downtime.
const latencyNote = "No blocks missed. Check flush_throttle_timeout (10ms), " +
	"peer_gossip_sleep_duration (10ms), timeout_commit (3s) and NTP sync."

func latencyLagField(ev latencyEval, windowMinutes int) internal.AlertField {
	return internal.AlertField{
		Name: fmt.Sprintf("precommit lag (%dm)", windowMinutes),
		Value: fmt.Sprintf("p50 %.0f ms / p90 %.0f ms (peers median p50: %.0f ms)",
			ev.Stat.P50, ev.Stat.P90, ev.PeerMedianP50),
	}
}

// buildLatencyAlertData renders one LATENCY alert. No mentions: a lagging
// validator misses no block, so it never pings a contact.
func buildLatencyAlertData(chainID string, ev latencyEval, windowMinutes int) internal.AlertData {
	fields := []internal.AlertField{
		{Name: "validator", Value: fmt.Sprintf("%s (%s)", ev.Stat.Moniker, ev.Stat.Addr)},
		latencyLagField(ev, windowMinutes),
	}
	if ev.Stat.LateRatio != nil {
		fields = append(fields, internal.AlertField{
			Name:  "late for quorum",
			Value: fmt.Sprintf("%.0f%% of %d signed blocks", *ev.Stat.LateRatio*100, ev.Stat.Samples),
		})
	}
	fields = append(fields,
		internal.AlertField{Name: "blocks", Value: fmt.Sprintf("%d -> %d", ev.Stat.MinHeight, ev.Stat.MaxHeight)},
		internal.AlertField{Name: "note", Value: latencyNote},
	)

	return internal.AlertData{
		ChainID: chainID,
		Level:   internal.AlertLatency,
		Emoji:   "🐢",
		Title:   "LATENCY — signing late for quorum",
		Fields:  fields,
	}
}

// buildLatencyResolvedData renders the paired LATENCY_RESOLVED notice.
func buildLatencyResolvedData(chainID string, ev latencyEval, windowMinutes int) internal.AlertData {
	return internal.AlertData{
		ChainID: chainID,
		Level:   internal.AlertResolved,
		Emoji:   "✅",
		Title:   "LATENCY RESOLVED",
		Fields: []internal.AlertField{
			{Name: "validator", Value: fmt.Sprintf("%s (%s)", ev.Stat.Moniker, ev.Stat.Addr)},
			latencyLagField(ev, windowMinutes),
		},
	}
}
