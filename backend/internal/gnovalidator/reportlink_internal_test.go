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

	// The legacy report_base_url key is no longer read.
	database.SetAdminConfig(db, "report_base_url", "https://memba.example")
	if got := reportLinkLine(db, "test12"); got != "" {
		t.Fatalf("legacy report_base_url should be ignored, got %q", got)
	}

	// A URL without placeholder is used verbatim.
	database.SetAdminConfig(db, "report_url", "https://memba.example/os/validators")
	got := reportLinkLine(db, "test12")
	want := "\n📊 Validator report: https://memba.example/os/validators"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// {chain} is replaced by the chain ID.
	database.SetAdminConfig(db, "report_url", " https://memba.example/reports/{chain} ")
	got = reportLinkLine(db, "test12")
	want = "\n📊 Validator report: https://memba.example/reports/test12"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Enabled but no report URL -> empty.
	db.Exec("DELETE FROM admin_configs WHERE key = 'report_url'")
	if got := reportLinkLine(db, "test12"); got != "" {
		t.Fatalf("no report url should be empty, got %q", got)
	}
}
