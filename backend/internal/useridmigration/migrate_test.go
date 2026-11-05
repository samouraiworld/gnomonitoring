package useridmigration

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	clerkAlice = "user_2alice"
	clerkBob   = "user_2bob"
	clerkCarol = "user_2carol"
	subC       = "33333333-3333-4333-8333-333333333333"
	emailAlice = "alice@example.com"
	emailBob   = "bob@example.com"
)

var (
	alice = Pair{ClerkUserID: clerkAlice, Sub: subA}
	bob   = Pair{ClerkUserID: clerkBob, Sub: subB}
)

// seedUser inserts one row under id in each of the five user-id tables.
func seedUser(t *testing.T, db *gorm.DB, id, email string) {
	t.Helper()
	require.NoError(t, db.Create(&database.User{UserID: id, Name: "name-" + id, Email: email}).Error)
	require.NoError(t, db.Create(&database.HourReport{UserID: id, DailyReportHour: 9, Timezone: "Europe/Paris"}).Error)
	wv := database.WebhookValidator{UserID: id, URL: "https://hooks.example.test/v/" + id, Type: "discord", Description: "validators"}
	require.NoError(t, db.Create(&wv).Error)
	require.NoError(t, db.Create(&database.WebhookGovDAO{UserID: id, URL: "https://hooks.example.test/g/" + id, Type: "slack", Description: "govdao"}).Error)
	addContact(t, db, id, wv.ID)
}

func addContact(t *testing.T, db *gorm.DB, id string, webhookID int) {
	t.Helper()
	require.NoError(t, db.Create(&database.AlertContact{UserID: id, Moniker: "moniker", NameContact: "contact", MentionTag: "<1>", IDwebhook: webhookID}).Error)
}

func countRows(t *testing.T, db *gorm.DB, table, id string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table(table).Where("user_id = ?", id).Count(&n).Error)
	return n
}

// requireRowsUnder asserts that id owns exactly want rows in every table.
func requireRowsUnder(t *testing.T, db *gorm.DB, id string, want int64) {
	t.Helper()
	for _, table := range Tables {
		require.Equal(t, want, countRows(t, db, table, id), "table %s, id %s", table, id)
	}
}

// snapshot returns sorted "<table>|<pk>|<user_id>" lines; users lines also
// carry the email, which the migration must never change.
func snapshot(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var lines []string
	type userRow struct{ UserID, Email string }
	var users []userRow
	require.NoError(t, db.Raw("SELECT user_id, email FROM users").Scan(&users).Error)
	for _, u := range users {
		lines = append(lines, fmt.Sprintf("users|%s|%s|%s", u.UserID, u.UserID, u.Email))
	}
	var hours []string
	require.NoError(t, db.Raw("SELECT user_id FROM hour_reports").Scan(&hours).Error)
	for _, id := range hours {
		lines = append(lines, fmt.Sprintf("hour_reports|%s|%s", id, id))
	}
	type idRow struct {
		ID     int
		UserID string
	}
	for _, table := range []string{"alert_contacts", "webhook_gov_daos", "webhook_validators"} {
		var rows []idRow
		require.NoError(t, db.Raw("SELECT id, user_id FROM "+table).Scan(&rows).Error)
		for _, r := range rows {
			lines = append(lines, fmt.Sprintf("%s|%d|%s", table, r.ID, r.UserID))
		}
	}
	sort.Strings(lines)
	return lines
}

func createErasureTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("CREATE TABLE account_erasures (digest varchar(64) PRIMARY KEY, erased_at timestamptz NOT NULL)").Error)
}

func insertErasure(t *testing.T, db *gorm.DB, userID string) {
	t.Helper()
	require.NoError(t, db.Exec("INSERT INTO account_erasures (digest, erased_at) VALUES (?, now())", ErasureDigest(userID)).Error)
}

func erasureDigests(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var digests []string
	require.NoError(t, db.Raw("SELECT digest FROM account_erasures ORDER BY digest").Scan(&digests).Error)
	return digests
}

func run(t *testing.T, db *gorm.DB, pairs []Pair, opts Options) *Report {
	t.Helper()
	report, err := Run(context.Background(), db, pairs, opts)
	require.NoError(t, err)
	return report
}

func userResult(t *testing.T, r *Report, from string) UserResult {
	t.Helper()
	for _, u := range r.Users {
		if u.From == from {
			return u
		}
	}
	t.Fatalf("no user result for %s", from)
	return UserResult{}
}

// seedConflictAndUnmapped builds the shared setup of the report tests: alice
// migrates, bob has an alert contact under both ids, carol is not mapped.
func seedConflictAndUnmapped(t *testing.T, db *gorm.DB) {
	t.Helper()
	seedUser(t, db, clerkAlice, emailAlice)
	seedUser(t, db, clerkBob, emailBob)
	addContact(t, db, subB, 0)
	seedUser(t, db, clerkCarol, "carol@example.com")
}

func TestRun_DryRunChangesNothing(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)
	before := snapshot(t, db)

	report := run(t, db, []Pair{alice}, Options{})

	assert.False(t, report.Applied)
	res := userResult(t, report, clerkAlice)
	assert.Equal(t, StatusMigrated, res.Status)
	for _, table := range Tables {
		assert.Equal(t, int64(1), res.Moved[table], table)
		assert.Equal(t, int64(1), report.MovedPerTable[table], table)
	}
	requireRowsUnder(t, db, clerkAlice, 1)
	requireRowsUnder(t, db, subA, 0)
	assert.Equal(t, before, snapshot(t, db))
}

func TestRun_ApplyMovesAllFiveTables(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)
	seedUser(t, db, clerkBob, emailBob)
	var contactIDs, govIDs, valIDs []int
	require.NoError(t, db.Raw("SELECT id FROM alert_contacts ORDER BY id").Scan(&contactIDs).Error)
	require.NoError(t, db.Raw("SELECT id FROM webhook_gov_daos ORDER BY id").Scan(&govIDs).Error)
	require.NoError(t, db.Raw("SELECT id FROM webhook_validators ORDER BY id").Scan(&valIDs).Error)

	report := run(t, db, []Pair{alice, bob}, Options{Apply: true})

	assert.True(t, report.Applied)
	for _, id := range []string{clerkAlice, clerkBob} {
		requireRowsUnder(t, db, id, 0)
	}
	for _, id := range []string{subA, subB} {
		requireRowsUnder(t, db, id, 1)
	}
	var email string
	require.NoError(t, db.Raw("SELECT email FROM users WHERE user_id = ?", subA).Scan(&email).Error)
	assert.Equal(t, emailAlice, email)
	var contactIDsAfter, govIDsAfter, valIDsAfter []int
	require.NoError(t, db.Raw("SELECT id FROM alert_contacts ORDER BY id").Scan(&contactIDsAfter).Error)
	require.NoError(t, db.Raw("SELECT id FROM webhook_gov_daos ORDER BY id").Scan(&govIDsAfter).Error)
	require.NoError(t, db.Raw("SELECT id FROM webhook_validators ORDER BY id").Scan(&valIDsAfter).Error)
	assert.Equal(t, contactIDs, contactIDsAfter)
	assert.Equal(t, govIDs, govIDsAfter)
	assert.Equal(t, valIDs, valIDsAfter)
	assert.Equal(t, int64(2), report.MovedPerTable["users"])
}

func TestRun_DryRunReportEqualsApplyReport(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedConflictAndUnmapped(t, db)

	dry := run(t, db, []Pair{alice, bob}, Options{})
	applied := run(t, db, []Pair{alice, bob}, Options{Apply: true})

	require.True(t, applied.Applied)
	applied.Applied = false
	assert.Equal(t, dry, applied)
}

func TestRun_IsIdempotent(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)

	run(t, db, []Pair{alice}, Options{Apply: true})
	afterFirst := snapshot(t, db)
	second := run(t, db, []Pair{alice}, Options{Apply: true})

	assert.Equal(t, StatusAlreadyMigrated, userResult(t, second, clerkAlice).Status)
	for table, moved := range second.MovedPerTable {
		assert.Zero(t, moved, table)
	}
	assert.Equal(t, afterFirst, snapshot(t, db))
}

func TestRun_ReverseRestoresOriginalIDs(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)
	seedUser(t, db, clerkBob, emailBob)
	s0 := snapshot(t, db)

	run(t, db, []Pair{alice, bob}, Options{Apply: true})
	reverse := run(t, db, []Pair{alice, bob}, Options{Apply: true, Reverse: true})

	assert.Equal(t, s0, snapshot(t, db))
	assert.True(t, reverse.Reverse)
	for _, sub := range []string{subA, subB} {
		res := userResult(t, reverse, sub)
		assert.Equal(t, StatusMigrated, res.Status)
	}
	assert.Equal(t, subA, reverse.Users[0].From)
	assert.Equal(t, clerkAlice, reverse.Users[0].To)
}

func TestRun_ConflictSkipsWholeUser(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkBob, emailBob)
	addContact(t, db, subB, 0)
	seedUser(t, db, clerkAlice, emailAlice)

	report := run(t, db, []Pair{alice, bob}, Options{Apply: true})

	res := userResult(t, report, clerkBob)
	assert.Equal(t, StatusConflict, res.Status)
	assert.Equal(t, []string{"alert_contacts"}, res.ConflictTables)
	assert.Empty(t, res.Moved)
	requireRowsUnder(t, db, clerkBob, 1)
	assert.Equal(t, int64(1), countRows(t, db, "alert_contacts", subB))
	assert.Equal(t, StatusMigrated, userResult(t, report, clerkAlice).Status)
	requireRowsUnder(t, db, subA, 1)
}

func TestRun_UnmappedIDsAreListedAndLeftAlone(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)
	seedUser(t, db, clerkCarol, "carol@example.com")
	seedUser(t, db, subC, "c3@example.com")

	report := run(t, db, []Pair{alice}, Options{Apply: true})

	require.Len(t, report.Unmapped, 2)
	assert.Equal(t, subC, report.Unmapped[0].UserID)
	assert.False(t, report.Unmapped[0].ClerkLike)
	assert.Equal(t, clerkCarol, report.Unmapped[1].UserID)
	assert.True(t, report.Unmapped[1].ClerkLike)
	assert.Equal(t, int64(1), report.Unmapped[1].Rows["users"])
	requireRowsUnder(t, db, clerkCarol, 1)
	requireRowsUnder(t, db, subC, 1)
}

func TestRun_RewritesErasureDigests(t *testing.T) {
	db := testoutils.NewTestDB(t)
	createErasureTable(t, db)
	insertErasure(t, db, clerkAlice)

	report := run(t, db, []Pair{alice}, Options{Apply: true})

	assert.True(t, report.ErasureTable)
	res := userResult(t, report, clerkAlice)
	assert.Equal(t, StatusMigrated, res.Status)
	assert.Equal(t, int64(1), res.Moved[ErasureTable])
	assert.Equal(t, int64(1), report.MovedPerTable[ErasureTable])
	assert.Equal(t, []string{ErasureDigest(subA)}, erasureDigests(t, db))

	run(t, db, []Pair{alice}, Options{Apply: true, Reverse: true})
	assert.Equal(t, []string{ErasureDigest(clerkAlice)}, erasureDigests(t, db))
}

func TestRun_ErasureMarkerUnderBothIDsIsAConflict(t *testing.T) {
	db := testoutils.NewTestDB(t)
	createErasureTable(t, db)
	insertErasure(t, db, clerkAlice)
	insertErasure(t, db, subA)
	seedUser(t, db, clerkAlice, emailAlice)

	report := run(t, db, []Pair{alice}, Options{Apply: true})

	res := userResult(t, report, clerkAlice)
	assert.Equal(t, StatusConflict, res.Status)
	assert.Contains(t, res.ConflictTables, ErasureTable)
	for table, moved := range report.MovedPerTable {
		assert.Zero(t, moved, table)
	}
	requireRowsUnder(t, db, clerkAlice, 1)
	assert.Len(t, erasureDigests(t, db), 2)
}

func TestRun_WithoutErasureTable(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)

	report := run(t, db, []Pair{alice}, Options{})

	assert.False(t, report.ErasureTable)
	_, present := report.MovedPerTable[ErasureTable]
	assert.False(t, present)
	var out bytes.Buffer
	require.NoError(t, report.WriteText(&out))
	assert.Contains(t, out.String(), "table account_erasures absent")
}

func TestRun_RefusesUnknownUserIDTable(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)
	require.NoError(t, db.Exec("CREATE TABLE extra_things (id serial PRIMARY KEY, user_id text)").Error)

	_, err := Run(context.Background(), db, []Pair{alice}, Options{Apply: true})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "extra_things")
	requireRowsUnder(t, db, clerkAlice, 1)
}

func TestRun_RefusesMissingUserIDTable(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)
	require.NoError(t, db.Exec("DROP TABLE hour_reports").Error)

	_, err := Run(context.Background(), db, []Pair{alice}, Options{Apply: true})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hour_reports")
	assert.Equal(t, int64(1), countRows(t, db, "users", clerkAlice))
}

func TestRun_RejectsInvalidPairs(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)

	for name, pairs := range map[string][]Pair{
		"no pairs":           nil,
		"duplicate clerk id": {alice, {ClerkUserID: clerkAlice, Sub: subB}},
		"duplicate sub":      {alice, {ClerkUserID: clerkBob, Sub: subA}},
		"swapped":            {{ClerkUserID: subA, Sub: clerkAlice}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Run(context.Background(), db, pairs, Options{Apply: true})
			assert.Error(t, err)
		})
	}
	requireRowsUnder(t, db, clerkAlice, 1)
}

func TestRun_FailsFastWhenTablesAreLocked(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedUser(t, db, clerkAlice, emailAlice)
	holder := db.Begin()
	require.NoError(t, holder.Error)
	require.NoError(t, holder.Exec("LOCK TABLE users IN ACCESS EXCLUSIVE MODE").Error)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, db, []Pair{alice}, Options{Apply: true, LockTimeout: 200 * time.Millisecond})
	elapsed := time.Since(start)
	require.NoError(t, holder.Rollback().Error)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock timeout")
	assert.Less(t, elapsed, 5*time.Second)
	requireRowsUnder(t, db, clerkAlice, 1)
	requireRowsUnder(t, db, subA, 0)
}

func TestReport_WriteText(t *testing.T) {
	db := testoutils.NewTestDB(t)
	seedConflictAndUnmapped(t, db)

	report := run(t, db, []Pair{alice, bob}, Options{})
	var out bytes.Buffer
	require.NoError(t, report.WriteText(&out))
	text := out.String()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	assert.Equal(t, "migrate-user-ids: mode=dry-run direction=forward pairs=2", lines[0])
	assert.Contains(t, lines, "table users moved=1")
	assert.Contains(t, lines, "users migrated=1 already-migrated=0 no-rows=0 conflict=1 unmapped=1")
	assert.Contains(t, lines, "MIGRATED user_2alice -> "+subA+": users=1 hour_reports=1 alert_contacts=1 webhook_gov_daos=1 webhook_validators=1")
	assert.Contains(t, lines, "CONFLICT user_2bob -> "+subB+": alert_contacts")
	assert.Contains(t, text, "\nUNMAPPED user_2carol clerk-like=true: users=1 ")
	assert.Equal(t, "result: rolled back (dry-run)", lines[len(lines)-1])
	assert.NotContains(t, text, "@example.com")

	applied := run(t, db, []Pair{alice, bob}, Options{Apply: true, Reverse: true})
	out.Reset()
	require.NoError(t, applied.WriteText(&out))
	text = out.String()
	assert.True(t, strings.HasPrefix(text, "migrate-user-ids: mode=apply direction=reverse pairs=2\n"), text)
	assert.True(t, strings.HasSuffix(text, "\nresult: committed\n"), text)
}
