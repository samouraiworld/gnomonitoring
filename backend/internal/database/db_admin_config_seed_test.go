package database_test

import (
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"gorm.io/gorm"
)

// latencySeedDefaults mirrors the documented defaults of the latency alert
// thresholds. The panel only renders keys returned by GetAllAdminConfigs, so
// these rows must exist for the "Latency Alerts" group to be tunable.
var latencySeedDefaults = map[string]string{
	"latency_alert_enabled":        "true",
	"latency_alert_check_minutes":  "5",
	"latency_alert_window_minutes": "60",
	"latency_alert_min_lag_ms":     "50",
	"latency_alert_peer_factor":    "3",
	"latency_alert_min_samples":    "300",
	"latency_alert_resend_hours":   "24",
}

// adminConfigMap flattens GetAllAdminConfigs into a key/value map.
func adminConfigMap(db *gorm.DB) (map[string]string, error) {
	configs, err := database.GetAllAdminConfigs(db)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(configs))
	for _, c := range configs {
		out[c.Key] = c.Value
	}
	return out, nil
}

func TestSeedAdminConfig_SeedsLatencyKeys(t *testing.T) {
	db := testoutils.NewTestDB(t)

	configs, err := adminConfigMap(db)
	if err != nil {
		t.Fatalf("GetAllAdminConfigs failed: %v", err)
	}

	for key, want := range latencySeedDefaults {
		got, ok := configs[key]
		if !ok {
			t.Errorf("key %q missing from admin_config after seeding", key)
			continue
		}
		if got != want {
			t.Errorf("key %q = %q, want %q", key, got, want)
		}
	}
}

func TestSeedAdminConfig_DoesNotOverwriteOperatorValue(t *testing.T) {
	db := testoutils.NewTestDB(t)

	if err := db.Model(&database.AdminConfig{}).
		Where("key = ?", "latency_alert_min_lag_ms").
		Update("value", "120").Error; err != nil {
		t.Fatalf("update failed: %v", err)
	}

	if err := database.SeedAdminConfig(db); err != nil {
		t.Fatalf("SeedAdminConfig failed: %v", err)
	}

	configs, err := adminConfigMap(db)
	if err != nil {
		t.Fatalf("GetAllAdminConfigs failed: %v", err)
	}
	if got := configs["latency_alert_min_lag_ms"]; got != "120" {
		t.Errorf("latency_alert_min_lag_ms = %q, want %q (operator value overwritten)", got, "120")
	}
}
