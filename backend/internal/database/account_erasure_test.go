package database

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"testing"
	"time"
)

// PostgreSQL is mandatory: SQLite cannot establish the locking invariant.
func erasureTestDB(t *testing.T) (*gorm.DB, func() *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN required for PostgreSQL erasure tests")
	}
	schema := fmt.Sprintf("erase_%d", time.Now().UnixNano())
	open := func(dsn string) *gorm.DB {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		return db
	}
	admin := open(dsn)
	require.NoError(t, admin.Exec(`CREATE SCHEMA "`+schema+`"`).Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec(`DROP SCHEMA "`+schema+`" CASCADE`).Error)
		p, _ := admin.DB()
		_ = p.Close()
	})
	connect := func() *gorm.DB {
		db := open(dsn + " search_path=" + schema)
		p, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = p.Close() })
		return db
	}
	db := connect()
	require.NoError(t, db.AutoMigrate(&AccountErasure{}, &User{}, &HourReport{}, &WebhookGovDAO{}, &WebhookValidator{}, &AlertContact{}))
	return db, connect
}
func TestAccountErasureDurableCascadeAndReset(t *testing.T) {
	db, reopen := erasureTestDB(t)
	const id = "clerk-original-identity"
	require.NoError(t, InsertUser(id, "a@example.test", "A", db))
	require.NoError(t, InsertWebhook(id, "https://discord.com/api/webhooks/1/a", "test", "discord", db))
	require.NoError(t, InsertMonitoringWebhook(id, "https://discord.com/api/webhooks/1/b", "test", "discord", "", db))
	require.NoError(t, InsertAlertContact(db, id, "validator", "contact", "", 0))
	require.NoError(t, InsertUser("other", "b@example.test", "B", db))
	require.NoError(t, EraseAccount(db, id))
	require.NoError(t, EraseAccount(db, id))
	require.NoError(t, DeleteUser(id, db)) // reset never removes the marker
	require.NoError(t, DeleteUser("other", db))
	require.NoError(t, InsertUser("other", "b@example.test", "B", db))
	for _, m := range []any{&User{}, &HourReport{}, &WebhookGovDAO{}, &WebhookValidator{}, &AlertContact{}} {
		var n int64
		require.NoError(t, db.Model(m).Where("user_id = ?", id).Count(&n).Error)
		require.Zero(t, n)
	}
	p, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, p.Close())
	db = reopen()
	writes := map[string]func() error{
		"user":        func() error { return InsertUser(id, "a@example.test", "A", db) },
		"user update": func() error { return UpdateUser(db, "A", "a@example.test", id) },
		"govdao":      func() error { return InsertWebhook(id, "url", "test", "discord", db) },
		"validator":   func() error { return InsertMonitoringWebhook(id, "url", "test", "discord", "", db) },
		"webhook update": func() error {
			return UpdateMonitoringWebhook(db, 1, id, "test", "url", "discord", nil, "webhook_validators")
		},
		"contact":        func() error { return InsertAlertContact(db, id, "validator", "contact", "", 0) },
		"contact update": func() error { return UpdateAlertContact(db, 1, id, "validator", "contact", "", 0) },
		"schedule":       func() error { return UpdateHeureReport(db, 10, 0, "UTC", id) },
		"admin schedule": func() error { return UpdateHourReportAdmin(db, id, 10, 0, "UTC") },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) { require.ErrorIs(t, write(), ErrAccountErased) })
	}
	var markers []AccountErasure
	require.NoError(t, db.Find(&markers).Error)
	require.Len(t, markers, 1)
	require.Len(t, markers[0].Digest, 64)
	require.NotContains(t, markers[0].Digest, id)
	require.False(t, markers[0].ErasedAt.IsZero())
	require.NoError(t, EraseAccount(db, "never-provisioned"))
	require.ErrorIs(t, InsertMonitoringWebhook("never-provisioned", "url", "test", "discord", "", db), ErrAccountErased)
}
func TestAccountErasureWaitsForWriteAcrossPools(t *testing.T) {
	db, connect := erasureTestDB(t)
	other := connect()
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- withActiveAccountWrite(db, "race", func(tx *gorm.DB) error {
			close(entered)
			<-release
			return tx.Create(&User{UserID: "race", Email: "r@example.test", Name: "R"}).Error
		})
	}()
	<-entered
	erased := make(chan error, 1)
	go func() { erased <- EraseAccount(other, "race") }()
	select {
	case err := <-erased:
		t.Fatalf("erasure passed pending writer: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-erased)
	var n int64
	require.NoError(t, other.Model(&User{}).Where("user_id = ?", "race").Count(&n).Error)
	require.Zero(t, n)
	require.ErrorIs(t, InsertUser("race", "r@example.test", "R", db), ErrAccountErased)
}
func TestAccountErasureWinsBeforeWaitingWrite(t *testing.T) {
	db, connect := erasureTestDB(t)
	other := connect()
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- withAccountLock(db, "race", func(tx *gorm.DB, digest string) error {
			if err := tx.Create(&AccountErasure{Digest: digest, ErasedAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
			close(entered)
			<-release
			return deleteUserRows(tx, "race")
		})
	}()
	<-entered
	written := make(chan error, 1)
	go func() { written <- InsertUser("race", "r@example.test", "R", other) }()
	select {
	case err := <-written:
		t.Fatalf("write passed pending erasure: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	require.ErrorIs(t, <-written, ErrAccountErased)
}
func TestAccountErasureRollsBackMarkerAndRowsTogether(t *testing.T) {
	db, _ := erasureTestDB(t)
	require.NoError(t, InsertUser("rollback", "r@example.test", "R", db))
	require.NoError(t, db.Exec(`CREATE TABLE deletion_blocker(user_id text REFERENCES users(user_id))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO deletion_blocker(user_id) VALUES (?)`, "rollback").Error)
	require.Error(t, EraseAccount(db, "rollback"))
	erased, err := IsAccountErased(db, "rollback")
	require.NoError(t, err)
	require.False(t, erased)
	var n int64
	require.NoError(t, db.Model(&HourReport{}).Where("user_id = ?", "rollback").Count(&n).Error)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.Exec(`DROP TABLE deletion_blocker`).Error)
	require.NoError(t, EraseAccount(db, "rollback"))
}
func TestAccountErasureRejectsInheritedOldSnapshot(t *testing.T) {
	db, _ := erasureTestDB(t)
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ").Error; err != nil {
			return err
		}
		return InsertUser("snapshot", "s@example.test", "S", tx)
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "read committed")
}

func TestAccountErasureOrphansAndOtherUserBackgroundAdminWrites(t *testing.T) {
	db, _ := erasureTestDB(t)
	// Monitoring historically permits child rows even without a users row.
	require.NoError(t, InsertMonitoringWebhook("orphan", "url", "test", "discord", "", db))
	require.NoError(t, InsertAlertContact(db, "orphan", "validator", "contact", "", 0))
	require.NoError(t, EraseAccount(db, "orphan"))
	for _, m := range []any{&HourReport{}, &WebhookValidator{}, &AlertContact{}} {
		var n int64
		require.NoError(t, db.Model(m).Where("user_id = ?", "orphan").Count(&n).Error)
		require.Zero(t, n)
	}
	require.NoError(t, InsertUser("other", "other@example.test", "Other", db))
	require.NoError(t, InsertWebhook("other", "shared-url", "test", "discord", db))
	require.NoError(t, UpdateLastCheckedID("shared-url", 42, db))
	var row WebhookGovDAO
	require.NoError(t, db.Where("user_id = ?", "other").Take(&row).Error)
	require.Equal(t, 42, row.LastCheckedID)
	require.NoError(t, ResetGovDAOLastCheckedID(db, row.ID))
	require.NoError(t, UpdateHourReportAdmin(db, "other", 12, 15, "UTC"))
	require.NoError(t, DeleteWebhookAdmin(db, "govdao", row.ID))
	var hr HourReport
	require.NoError(t, db.Where("user_id = ?", "other").Take(&hr).Error)
	require.Equal(t, 12, hr.DailyReportHour)
}
