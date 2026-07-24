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
