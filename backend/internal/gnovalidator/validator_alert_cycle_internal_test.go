package gnovalidator

import (
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func alertCycleTestThresholds() Thresholds {
	return Thresholds{
		WarningThreshold:         5,
		CriticalThreshold:        30,
		AlertWarningResendHours:  6,
		AlertCriticalResendHours: 24,
		DeadValidatorSilenceDays: 7,
	}
}

// captureValidatorAlertDispatch replaces the notification of WARNING/CRITICAL
// alerts for the test and returns the levels dispatched, in order.
func captureValidatorAlertDispatch(t *testing.T) *[]string {
	t.Helper()
	var levels []string
	setValidatorAlertDispatch(func(_ string, _ int, _, level, _, _ string, _, _ int64, _ *gorm.DB) error {
		levels = append(levels, level)
		return nil
	})
	t.Cleanup(func() { setValidatorAlertDispatch(defaultValidatorAlertDispatch) })
	return &levels
}

// seedParticipationRows writes one daily_participations row per height in
// [from, to], dated at, for addr.
func seedParticipationRows(t *testing.T, db *gorm.DB, chainID, addr string, from, to int64, participated bool, at time.Time) {
	t.Helper()
	for h := from; h <= to; h++ {
		require.NoError(t, db.Create(&database.DailyParticipation{
			ChainID: chainID, Addr: addr, Moniker: "val", BlockHeight: h,
			Date: at, Participated: participated,
		}).Error)
	}
}

// alertRows returns addr's alert_logs rows, oldest first.
func alertRows(t *testing.T, db *gorm.DB, chainID, addr string) []database.AlertLog {
	t.Helper()
	var rows []database.AlertLog
	require.NoError(t, db.Where("chain_id = ? AND addr = ?", chainID, addr).
		Order("sent_at, id").Find(&rows).Error)
	return rows
}

func alertLevels(t *testing.T, db *gorm.DB, chainID, addr string) []string {
	t.Helper()
	var levels []string
	for _, r := range alertRows(t, db, chainID, addr) {
		levels = append(levels, r.Level)
	}
	return levels
}

// A validator that joins the valset and stops before its first signature has
// no participation in the silence window. Its WARNING must not turn the
// CRITICAL of the same incident into a repeat silenced as a dead validator.
func TestRunValidatorAlertCycle_FirstIncidentCriticalNotSilenced(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "alert-cycle-first-incident"
	sent := captureValidatorAlertDispatch(t)
	th := alertCycleTestThresholds()
	now := time.Now().UTC()

	seedParticipationRows(t, db, chainID, "g1new", 100, 104, false, now)
	runValidatorAlertCycle(db, chainID, th)
	require.Equal(t, []string{"WARNING"}, *sent)

	seedParticipationRows(t, db, chainID, "g1new", 105, 134, false, now)
	runValidatorAlertCycle(db, chainID, th)
	assert.Equal(t, []string{"WARNING", "CRITICAL"}, *sent,
		"the CRITICAL of a validator's first incident must be sent while it is down")

	// Recovery: the incident is closed by a RESOLVED, not by a late CRITICAL.
	seedParticipationRows(t, db, chainID, "g1new", 135, 135, true, now)
	runValidatorAlertCycle(db, chainID, th)
	SendResolveAlerts(db, chainID)
	assert.Equal(t, []string{"WARNING", "CRITICAL"}, *sent)
	assert.Equal(t, []string{"WARNING", "CRITICAL", "RESOLVED"}, alertLevels(t, db, chainID, "g1new"))
}

// The WARNING of a first incident does not silence its CRITICAL even once the
// WARNING's blocks have left the 30-minute window GetMissedWindows reads.
func TestRunValidatorAlertCycle_FirstIncidentCriticalNotSilencedAfterWindowSlides(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "alert-cycle-window-slide"
	sent := captureValidatorAlertDispatch(t)
	old := time.Now().UTC().Add(-40 * time.Minute)

	seedParticipationRows(t, db, chainID, "g1new", 100, 104, false, old)
	require.NoError(t, database.InsertAlertlog(db, chainID, "g1new", "val", "WARNING", 100, 104, true, old, ""))
	seedParticipationRows(t, db, chainID, "g1new", 105, 140, false, time.Now().UTC())
	runValidatorAlertCycle(db, chainID, alertCycleTestThresholds())

	assert.Equal(t, []string{"CRITICAL"}, *sent)
}

// The very first alert of a validator that never signed always goes through.
func TestRunValidatorAlertCycle_FirstEverAlertSent(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "alert-cycle-first-ever"
	sent := captureValidatorAlertDispatch(t)

	seedParticipationRows(t, db, chainID, "g1never", 100, 140, false, time.Now().UTC())
	runValidatorAlertCycle(db, chainID, alertCycleTestThresholds())

	assert.Equal(t, []string{"CRITICAL"}, *sent)
	assert.Equal(t, []string{"CRITICAL"}, alertLevels(t, db, chainID, "g1never"))
}

// A validator that already had a CRITICAL and has not signed in the silence
// window stays silent on a new one.
func TestRunValidatorAlertCycle_DeadRepeatValidatorSilenced(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "alert-cycle-dead-repeat"
	sent := captureValidatorAlertDispatch(t)
	longAgo := time.Now().UTC().Add(-10 * 24 * time.Hour)

	require.NoError(t, database.InsertAlertlog(db, chainID, "g1dead", "val", "CRITICAL", 10, 50, true, longAgo, ""))
	seedParticipationRows(t, db, chainID, "g1dead", 100, 140, false, time.Now().UTC())
	runValidatorAlertCycle(db, chainID, alertCycleTestThresholds())

	assert.Empty(t, *sent, "no new alert for a repeat validator with no participation in the silence window")
	assert.Equal(t, []string{"CRITICAL"}, alertLevels(t, db, chainID, "g1dead"))
}

// A missed sequence the validator already recovered from before any check saw
// it is recorded with its RESOLVED, but never notified.
func TestRunValidatorAlertCycle_ClosedIncidentRecordedNotNotified(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "alert-cycle-closed"
	sent := captureValidatorAlertDispatch(t)
	now := time.Now().UTC()

	seedParticipationRows(t, db, chainID, "g1blip", 1, 50, true, now)
	seedParticipationRows(t, db, chainID, "g1blip", 100, 140, false, now)
	seedParticipationRows(t, db, chainID, "g1blip", 141, 141, true, now)
	runValidatorAlertCycle(db, chainID, alertCycleTestThresholds())
	SendResolveAlerts(db, chainID)

	assert.Empty(t, *sent)
	rows := alertRows(t, db, chainID, "g1blip")
	require.Len(t, rows, 2)
	assert.Equal(t, "CRITICAL", rows[0].Level)
	assert.False(t, rows[0].Skipped, "an alert recorded without notification is not marked dispatched")
	assert.EqualValues(t, 100, rows[0].StartHeight)
	assert.EqualValues(t, 140, rows[0].EndHeight)
	assert.Equal(t, "RESOLVED", rows[1].Level)

	// The next cycle does not record the same sequence again.
	runValidatorAlertCycle(db, chainID, alertCycleTestThresholds())
	assert.Len(t, alertRows(t, db, chainID, "g1blip"), 2)
}

// A WARNING sent while the sequence was open, then a sequence that crossed
// the CRITICAL threshold and closed between two checks: the CRITICAL is
// recorded without notification and the recovery is announced once.
func TestRunValidatorAlertCycle_ClosedAfterWarningRecordsCritical(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "alert-cycle-closed-after-warning"
	sent := captureValidatorAlertDispatch(t)
	th := alertCycleTestThresholds()
	now := time.Now().UTC()

	seedParticipationRows(t, db, chainID, "g1val", 1, 50, true, now)
	seedParticipationRows(t, db, chainID, "g1val", 100, 105, false, now)
	runValidatorAlertCycle(db, chainID, th)
	require.Equal(t, []string{"WARNING"}, *sent)

	seedParticipationRows(t, db, chainID, "g1val", 106, 135, false, now)
	seedParticipationRows(t, db, chainID, "g1val", 136, 136, true, now)
	runValidatorAlertCycle(db, chainID, th)
	SendResolveAlerts(db, chainID)

	assert.Equal(t, []string{"WARNING"}, *sent, "no CRITICAL for an incident already over")
	rows := alertRows(t, db, chainID, "g1val")
	require.Len(t, rows, 3)
	assert.Equal(t, "CRITICAL", rows[1].Level)
	assert.True(t, rows[1].Skipped, "joins the dispatched WARNING so SendResolveAlerts announces the recovery")
	assert.Equal(t, "RESOLVED", rows[2].Level)
	assert.EqualValues(t, 135, rows[2].EndHeight)
}

// A repeat validator silenced as dead while down is not notified when it
// comes back: its first signature closes the incident.
func TestRunValidatorAlertCycle_DeadRepeatValidatorNotNotifiedOnRecovery(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "alert-cycle-dead-recovers"
	sent := captureValidatorAlertDispatch(t)
	longAgo := time.Now().UTC().Add(-10 * 24 * time.Hour)

	require.NoError(t, database.InsertAlertlog(db, chainID, "g1dead", "val", "CRITICAL", 10, 50, true, longAgo, ""))
	seedParticipationRows(t, db, chainID, "g1dead", 100, 140, false, time.Now().UTC())
	runValidatorAlertCycle(db, chainID, alertCycleTestThresholds())
	require.Empty(t, *sent)

	seedParticipationRows(t, db, chainID, "g1dead", 141, 141, true, time.Now().UTC())
	runValidatorAlertCycle(db, chainID, alertCycleTestThresholds())

	assert.Empty(t, *sent)
}

// A same-level alert sent within the resend window, with no RESOLVED since,
// suppresses a new one for the same incident.
func TestRunValidatorAlertCycle_ResendDedup(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "alert-cycle-dedup"
	sent := captureValidatorAlertDispatch(t)
	now := time.Now().UTC()

	seedParticipationRows(t, db, chainID, "g1down", 1, 50, true, now)
	seedParticipationRows(t, db, chainID, "g1down", 100, 140, false, now)
	require.NoError(t, database.InsertAlertlog(db, chainID, "g1down", "val", "CRITICAL", 100, 135, true, now.Add(-time.Hour), ""))
	runValidatorAlertCycle(db, chainID, alertCycleTestThresholds())

	assert.Empty(t, *sent)
	assert.Equal(t, []string{"CRITICAL"}, alertLevels(t, db, chainID, "g1down"))
}
