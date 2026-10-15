package gnovalidator

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestLoadThresholds_GapReconciliationDefaults(t *testing.T) {
	db := testoutils.NewTestDB(t)
	LoadThresholds(db)
	got := GetThresholds()

	if got.GapReconciliationIntervalSeconds != 3600 {
		t.Errorf("GapReconciliationIntervalSeconds = %d, want default 3600", got.GapReconciliationIntervalSeconds)
	}
	if got.GapReconciliationLookbackDays != 7 {
		t.Errorf("GapReconciliationLookbackDays = %d, want default 7", got.GapReconciliationLookbackDays)
	}
	if got.GapReconciliationInterval() != time.Hour {
		t.Errorf("GapReconciliationInterval() = %v, want 1h", got.GapReconciliationInterval())
	}
}

func TestThresholds_LatencyDefaults(t *testing.T) {
	d := Thresholds{
		LatencyAlertEnabled:       true,
		LatencyAlertCheckMinutes:  5,
		LatencyAlertWindowMinutes: 60,
		LatencyAlertMinLagMs:      50,
		LatencyAlertPeerFactor:    3,
		LatencyAlertMinSamples:    300,
		LatencyAlertResendHours:   24,
	}
	if got := GetThresholds(); got.LatencyAlertCheckMinutes != d.LatencyAlertCheckMinutes ||
		got.LatencyAlertWindowMinutes != d.LatencyAlertWindowMinutes ||
		got.LatencyAlertMinLagMs != d.LatencyAlertMinLagMs ||
		got.LatencyAlertPeerFactor != d.LatencyAlertPeerFactor ||
		got.LatencyAlertMinSamples != d.LatencyAlertMinSamples ||
		got.LatencyAlertResendHours != d.LatencyAlertResendHours ||
		!got.LatencyAlertEnabled {
		t.Fatalf("package defaults = %+v, want the documented latency defaults", got)
	}
	if got := d.LatencyAlertCheckInterval(); got != 5*time.Minute {
		t.Errorf("LatencyAlertCheckInterval() = %v, want 5m", got)
	}
}

func TestSanitizeLatencyThresholds_FallsBackOnNonPositive(t *testing.T) {
	in := Thresholds{
		LatencyAlertCheckMinutes:  0,
		LatencyAlertWindowMinutes: -1,
		LatencyAlertMinLagMs:      0,
		LatencyAlertPeerFactor:    0,
		LatencyAlertMinSamples:    -5,
		LatencyAlertResendHours:   0,
	}
	got := sanitizeLatencyThresholds(in)
	if got.LatencyAlertCheckMinutes != 5 || got.LatencyAlertWindowMinutes != 60 ||
		got.LatencyAlertMinLagMs != 50 || got.LatencyAlertPeerFactor != 3 ||
		got.LatencyAlertMinSamples != 300 || got.LatencyAlertResendHours != 24 {
		t.Fatalf("sanitizeLatencyThresholds(%+v) = %+v, want every non-positive value replaced by its default", in, got)
	}
}

func TestSanitizeBFTThresholds(t *testing.T) {
	def := Thresholds{
		BFTAlertCheckMinutes: 5, BFTAlertWindowBlocks: 100, BFTAlertWarningLosses: 1,
		BFTAlertWarningMarginPct: 10, BFTAlertCriticalMarginPct: 3,
	}
	for _, in := range []Thresholds{
		{BFTAlertCheckMinutes: 0, BFTAlertWindowBlocks: 0, BFTAlertWarningLosses: -1, BFTAlertWarningMarginPct: -1, BFTAlertCriticalMarginPct: -1},
		{BFTAlertCheckMinutes: 1441, BFTAlertWindowBlocks: 10001, BFTAlertWarningLosses: -5, BFTAlertWarningMarginPct: 34, BFTAlertCriticalMarginPct: 33.4},
	} {
		if got := sanitizeBFTThresholds(in); got != def {
			t.Errorf("sanitizeBFTThresholds(%+v) = %+v, want defaults %+v", in, got, def)
		}
	}

	ok := Thresholds{BFTAlertCheckMinutes: 1440, BFTAlertWindowBlocks: 10000, BFTAlertWarningLosses: 0,
		BFTAlertWarningMarginPct: 33, BFTAlertCriticalMarginPct: 0}
	if got := sanitizeBFTThresholds(ok); got != ok {
		t.Errorf("sanitizeBFTThresholds(%+v) = %+v, want unchanged", ok, got)
	}
}
