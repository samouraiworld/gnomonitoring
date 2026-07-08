package gnovalidator

import (
	"testing"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
)

func TestReportLinkLine(t *testing.T) {
	db := testoutils.NewTestDB(t)

	// Disabled by default -> empty.
	if got := reportLinkLine(db, "test12"); got != "" {
		t.Fatalf("disabled should be empty, got %q", got)
	}

	database.SetAdminConfig(db, "validator_report_enabled.test12", "true")
	database.SetAdminConfig(db, "report_base_url", "https://memba.example")

	got := reportLinkLine(db, "test12")
	want := "\n📊 Validator report: https://memba.example/reports/test12"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Enabled but no base URL -> empty.
	database.SetAdminConfig(db, "validator_report_enabled.test12", "true")
	db.Exec("DELETE FROM admin_configs WHERE key = 'report_base_url'")
	if got := reportLinkLine(db, "test12"); got != "" {
		t.Fatalf("no base url should be empty, got %q", got)
	}
}
