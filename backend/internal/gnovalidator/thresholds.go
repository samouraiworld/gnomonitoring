package gnovalidator

import (
	"log"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

// Thresholds holds all tunable alert and monitoring parameters.
// Values are loaded from the admin_config DB table and cached in memory.
// Use GetThresholds() for thread-safe reads; call RefreshThresholds(db) after a write.
type Thresholds struct {
	WarningThreshold            int
	CriticalThreshold           int
	AlertCriticalResendHours    int
	AlertWarningResendHours     int
	StagnationFirstAlertSeconds int
	StagnationRepeatMinutes     int
	RPCErrorCooldownMinutes     int
	NewValidatorScanMinutes     int
	AlertCheckIntervalSeconds   int
	RawRetentionDays            int
	AggregatorPeriodMinutes     int
	RecentBlocksWindow          int
}

var (
	activeThresholds = Thresholds{
		WarningThreshold:            5,
		CriticalThreshold:           30,
		AlertCriticalResendHours:    24,
		AlertWarningResendHours:     6,
		StagnationFirstAlertSeconds: 20,
		StagnationRepeatMinutes:     30,
		RPCErrorCooldownMinutes:     10,
		NewValidatorScanMinutes:     5,
		AlertCheckIntervalSeconds:   20,
		RawRetentionDays:            7,
		AggregatorPeriodMinutes:     60,
		RecentBlocksWindow:          50,
	}
	thresholdsMu sync.RWMutex
)

// LoadThresholds reads all threshold values from the admin_config table and
// updates the in-memory cache. Called once at startup and after each admin write.
func LoadThresholds(db *gorm.DB) {
	thresholdsMu.Lock()
	defer thresholdsMu.Unlock()
	activeThresholds = Thresholds{
		WarningThreshold:            database.GetAdminConfigInt(db, "warning_threshold", 5),
		CriticalThreshold:           database.GetAdminConfigInt(db, "critical_threshold", 30),
		AlertCriticalResendHours:    database.GetAdminConfigInt(db, "alert_critical_resend_hours", 24),
		AlertWarningResendHours:     database.GetAdminConfigInt(db, "alert_warning_resend_hours", 6),
		StagnationFirstAlertSeconds: database.GetAdminConfigInt(db, "stagnation_first_alert_seconds", 20),
		StagnationRepeatMinutes:     database.GetAdminConfigInt(db, "stagnation_repeat_minutes", 30),
		RPCErrorCooldownMinutes:     database.GetAdminConfigInt(db, "rpc_error_cooldown_minutes", 10),
		NewValidatorScanMinutes:     database.GetAdminConfigInt(db, "new_validator_scan_minutes", 5),
		AlertCheckIntervalSeconds:   database.GetAdminConfigInt(db, "alert_check_interval_seconds", 20),
		RawRetentionDays:            database.GetAdminConfigInt(db, "raw_retention_days", 7),
		AggregatorPeriodMinutes:     database.GetAdminConfigInt(db, "aggregator_period_minutes", 60),
		RecentBlocksWindow:          database.GetAdminConfigInt(db, "recent_blocks_window", 50),
	}
	log.Printf("[thresholds] loaded: warning=%d critical=%d resend_critical=%dh resend_warning=%dh stagnation_first=%ds stagnation_repeat=%dmin",
		activeThresholds.WarningThreshold,
		activeThresholds.CriticalThreshold,
		activeThresholds.AlertCriticalResendHours,
		activeThresholds.AlertWarningResendHours,
		activeThresholds.StagnationFirstAlertSeconds,
		activeThresholds.StagnationRepeatMinutes,
	)
}

// GetThresholds returns a snapshot of the current threshold values.
func GetThresholds() Thresholds {
	thresholdsMu.RLock()
	defer thresholdsMu.RUnlock()
	return activeThresholds
}

// RefreshThresholds reloads thresholds from DB. Called by admin PUT /admin/config/thresholds.
func RefreshThresholds(db *gorm.DB) {
	LoadThresholds(db)
}

// Helper methods to convert to time.Duration.

func (t Thresholds) StagnationFirstAlert() time.Duration {
	return time.Duration(t.StagnationFirstAlertSeconds) * time.Second
}

func (t Thresholds) StagnationRepeat() time.Duration {
	return time.Duration(t.StagnationRepeatMinutes) * time.Minute
}

func (t Thresholds) RPCErrorCooldown() time.Duration {
	return time.Duration(t.RPCErrorCooldownMinutes) * time.Minute
}

func (t Thresholds) AlertCheckInterval() time.Duration {
	return time.Duration(t.AlertCheckIntervalSeconds) * time.Second
}

func (t Thresholds) NewValidatorScan() time.Duration {
	return time.Duration(t.NewValidatorScanMinutes) * time.Minute
}

func (t Thresholds) AggregatorPeriod() time.Duration {
	return time.Duration(t.AggregatorPeriodMinutes) * time.Minute
}

// ResendHoursForLevel returns the minimum hours between two alerts of the same level
// for the same validator. CRITICAL: 24h, WARNING: 6h (configurable via admin_config).
func (t Thresholds) ResendHoursForLevel(level string) int {
	if level == "CRITICAL" {
		return t.AlertCriticalResendHours
	}
	return t.AlertWarningResendHours
}
