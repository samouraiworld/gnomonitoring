package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const subA = "11111111-1111-4111-8111-111111111111"

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestRun_RequiresMapping(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 2, run(nil, &out, &errb))
	assert.Contains(t, errb.String(), "--mapping is required")
}

func TestRun_HelpExitsZero(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 0, run([]string{"-h"}, &out, &errb))
	assert.Contains(t, out.String(), "--apply")
	assert.Contains(t, out.String(), "dry-run")
}

func TestRun_UnknownFlagIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 2, run([]string{"--mapping", "m.csv", "--dry-run-please"}, &out, &errb))
	assert.Contains(t, errb.String(), "Usage:")
}

func TestRun_PositionalArgumentIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 2, run([]string{"--mapping", "m.csv", "apply"}, &out, &errb))
}

func TestRun_NegativeLockTimeoutIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 2, run([]string{"--mapping", "m.csv", "--lock-timeout", "-1s"}, &out, &errb))
}

func TestRun_MissingMappingFileFails(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 1, run([]string{"--mapping", "/nonexistent.csv", "--dsn", "host=x"}, &out, &errb))
}

func TestRun_InvalidMappingFailsBeforeConnecting(t *testing.T) {
	mapping := writeFile(t, "mapping.csv", "clerk_user_id,sub\nuser_2alice,"+subA+"\nuser_2bob,"+subA+"\n")
	var out, errb bytes.Buffer
	code := run([]string{"--mapping", mapping, "--dsn", "host=127.0.0.1 port=1 connect_timeout=1"}, &out, &errb)
	assert.Equal(t, 1, code)
	assert.Contains(t, errb.String(), "duplicate sub")
	assert.NotContains(t, errb.String(), "connect")
}

func TestRun_MissingConfigFails(t *testing.T) {
	mapping := writeFile(t, "mapping.csv", "clerk_user_id,sub\nuser_2alice,"+subA+"\n")
	var out, errb bytes.Buffer
	code := run([]string{"--mapping", mapping, "--config", filepath.Join(t.TempDir(), "absent.yaml")}, &out, &errb)
	assert.Equal(t, 1, code)
	assert.Contains(t, errb.String(), "absent.yaml")
}

const databaseYAML = `database:
  host: postgres
  port: 5432
  user: gnomonitoring
  password: pw
  dbname: gnomonitoring
`

func TestDSNFromConfig_ReadsDatabaseBlock(t *testing.T) {
	dsn, err := dsnFromConfig(writeFile(t, "config.yaml", databaseYAML))
	require.NoError(t, err)
	assert.Equal(t, "host=postgres port=5432 user=gnomonitoring password=pw dbname=gnomonitoring sslmode=disable", dsn)
}

func TestDSNFromConfig_IgnoresAuthAndChains(t *testing.T) {
	dsn, err := dsnFromConfig(writeFile(t, "config.yaml", "auth_provider: \"clerk\"\nclerk_secret_key: \"x\"\n"+databaseYAML))
	require.NoError(t, err)
	assert.Equal(t, "host=postgres port=5432 user=gnomonitoring password=pw dbname=gnomonitoring sslmode=disable", dsn)
}

func TestDSNFromConfig_MissingDatabaseBlockErrors(t *testing.T) {
	_, err := dsnFromConfig(writeFile(t, "config.yaml", "backend_port: \"8989\"\n"))
	assert.Error(t, err)
}

// schemaDSN returns a DSN that reaches the isolated schema NewTestDB created.
func schemaDSN(t *testing.T, db *gorm.DB) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_DSN")
	if base == "" {
		base = "host=localhost port=5432 user=gnomonitoring password=gnomonitoring dbname=gnomonitoring_test sslmode=disable"
	}
	var schema string
	require.NoError(t, db.Raw("SELECT current_schema()").Scan(&schema).Error)
	return base + " search_path=" + schema
}

func usersUnder(t *testing.T, db *gorm.DB, id string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table("users").Where("user_id = ?", id).Count(&n).Error)
	return n
}

func TestRun_DryRunApplyAndReverseEndToEnd(t *testing.T) {
	db := testoutils.NewTestDB(t)
	require.NoError(t, db.Create(&database.User{UserID: "user_2alice", Name: "alice", Email: "alice@example.com"}).Error)
	require.NoError(t, db.Create(&database.WebhookValidator{UserID: "user_2alice", URL: "https://hooks.example.test/v", Type: "discord"}).Error)
	mapping := writeFile(t, "mapping.csv", "clerk_user_id,sub\nuser_2alice,"+subA+"\n")
	dsn := schemaDSN(t, db)

	var out, errb bytes.Buffer
	require.Equal(t, 0, run([]string{"--mapping", mapping, "--dsn", dsn}, &out, &errb), errb.String())
	assert.Contains(t, out.String(), "migrate-user-ids: mode=dry-run direction=forward pairs=1\n")
	assert.Contains(t, out.String(), "MIGRATED user_2alice -> "+subA+": users=1 webhook_validators=1\n")
	assert.Contains(t, out.String(), "result: rolled back (dry-run)\n")
	assert.Equal(t, int64(1), usersUnder(t, db, "user_2alice"))
	assert.Equal(t, int64(0), usersUnder(t, db, subA))

	out.Reset()
	require.Equal(t, 0, run([]string{"--mapping", mapping, "--dsn", dsn, "--apply"}, &out, &errb), errb.String())
	assert.Contains(t, out.String(), "result: committed\n")
	assert.Equal(t, int64(0), usersUnder(t, db, "user_2alice"))
	assert.Equal(t, int64(1), usersUnder(t, db, subA))

	out.Reset()
	require.Equal(t, 0, run([]string{"--mapping", mapping, "--dsn", dsn, "--apply", "--reverse"}, &out, &errb), errb.String())
	assert.Contains(t, out.String(), "direction=reverse")
	assert.Equal(t, int64(1), usersUnder(t, db, "user_2alice"))
	assert.Equal(t, int64(0), usersUnder(t, db, subA))
	assert.NotContains(t, out.String()+errb.String(), "example.com")
}

func TestRun_DatabaseErrorExitsOne(t *testing.T) {
	db := testoutils.NewTestDB(t)
	require.NoError(t, db.Exec("CREATE TABLE extra_things (id serial PRIMARY KEY, user_id text)").Error)
	mapping := writeFile(t, "mapping.csv", "clerk_user_id,sub\nuser_2alice,"+subA+"\n")

	var out, errb bytes.Buffer
	assert.Equal(t, 1, run([]string{"--mapping", mapping, "--dsn", schemaDSN(t, db), "--apply"}, &out, &errb))
	assert.Contains(t, errb.String(), "extra_things")
	assert.Empty(t, out.String())
}
