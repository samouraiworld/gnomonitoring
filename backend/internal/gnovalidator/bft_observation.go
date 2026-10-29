package gnovalidator

import (
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

// ObserveBFTWindow exposes the alert evaluator's live margin without sending
// alerts or changing its hysteresis. An unavailable observation carries a stable
// reason instead of a synthetic zero margin. Callers use the same thresholds
// and bounded window as WatchBFTAlerts, never the daily report's 24-hour window.
func ObserveBFTWindow(w database.BFTWindow, t Thresholds, now time.Time) (BFTMargin, string) {
	if w.Heights == 0 {
		return BFTMargin{}, "no_data"
	}
	if span := w.MaxHeight - w.MinHeight + 1; 5*w.Heights < 4*span {
		return BFTMargin{}, "insufficient_coverage"
	}
	if now.Sub(w.LastBlockTime) > bftStaleAfter(t) {
		return BFTMargin{}, "stale"
	}
	ev := evaluateBFT(w, t, now)
	if !ev.Evaluated {
		return BFTMargin{}, "no_voting_power"
	}
	return ev.Live, ""
}
