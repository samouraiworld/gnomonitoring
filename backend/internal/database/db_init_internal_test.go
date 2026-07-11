package database

import "testing"

func TestEnsureUTCTimeZone_AppendsWhenAbsent(t *testing.T) {
	got := ensureUTCTimeZone("host=localhost port=5432 user=x dbname=y sslmode=disable")
	want := "host=localhost port=5432 user=x dbname=y sslmode=disable TimeZone=UTC"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEnsureUTCTimeZone_RespectsCallerOverride(t *testing.T) {
	dsn := "host=localhost port=5432 user=x dbname=y sslmode=disable TimeZone=Europe/Paris"
	if got := ensureUTCTimeZone(dsn); got != dsn {
		t.Fatalf("got %q, want unchanged %q", got, dsn)
	}
}

func TestEnsureUTCTimeZone_CaseInsensitiveDetection(t *testing.T) {
	dsn := "host=localhost port=5432 user=x dbname=y sslmode=disable timezone=Europe/Paris"
	if got := ensureUTCTimeZone(dsn); got != dsn {
		t.Fatalf("got %q, want unchanged %q (lowercase timezone= must still be detected)", got, dsn)
	}
}
