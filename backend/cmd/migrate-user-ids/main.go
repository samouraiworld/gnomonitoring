// Command migrate-user-ids rewrites the user ids gnomonitoring stores, from
// Clerk user ids to Keycloak subs (or back with --reverse), in one database
// transaction. It is a dry-run unless --apply is given. Run it with the
// backend stopped. The mapping file holds personal identifiers: never commit
// it or paste it anywhere.
//
// See package useridmigration for what the tool changes and reports.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/useridmigration"
	"gopkg.in/yaml.v2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const usage = `Usage: migrate-user-ids --mapping FILE [--dsn DSN | --config PATH] [--apply] [--reverse] [--lock-timeout DURATION]

Rewrites the user ids stored by gnomonitoring from Clerk user ids to Keycloak
subs, in the tables users, hour_reports, alert_contacts, webhook_gov_daos and
webhook_validators, plus the account_erasures digests when that table exists.
Everything runs in one transaction.

By default it is a dry-run: it performs the same updates, prints the report
and rolls back, so nothing changes. Pass --apply to commit. --reverse rewrites
subs back to Clerk ids with the same mapping.

Stop the backend before running it. Apply only right before starting a server
that identifies users by their Keycloak sub: a server that still uses Clerk ids
would show migrated users empty lists and store new rows under the old ids.

Never commit the mapping file or paste it anywhere: it holds personal
identifiers. The report repeats them (MIGRATED lines pair each Clerk id with
its sub); handle it like the mapping.

Flags:
  --mapping FILE           CSV with the header clerk_user_id,sub (required)
  --dsn DSN                Postgres DSN; overrides --config. It shows in the
                           process list: leave the password out and set
                           PGPASSWORD, or prefer --config
  --config PATH            config file whose database: block gives the DSN (default config.yaml)
  --apply                  commit the changes (default: dry-run)
  --reverse                rewrite subs back to Clerk ids
  --lock-timeout DURATION  how long to wait for the table locks (default 10s)

Exit codes: 0 done (conflicts and unmapped ids are reported, not failures),
1 runtime or validation error, 2 usage error.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate-user-ids", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mappingPath := fs.String("mapping", "", "")
	dsnFlag := fs.String("dsn", "", "")
	configPath := fs.String("config", "config.yaml", "")
	apply := fs.Bool("apply", false, "")
	reverse := fs.Bool("reverse", false, "")
	lockTimeout := fs.Duration("lock-timeout", 10*time.Second, "")

	usageError := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "migrate-user-ids: "+format+"\n\n", a...)
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		return usageError("%v", err)
	}
	if fs.NArg() > 0 {
		// The value is not echoed: an unquoted --dsn spills its password here.
		return usageError("unexpected argument after the flags (quote the --dsn value)")
	}
	if *mappingPath == "" {
		return usageError("--mapping is required")
	}
	if *lockTimeout <= 0 {
		return usageError("--lock-timeout must be positive")
	}

	fail := func(err error) int {
		fmt.Fprintf(stderr, "migrate-user-ids: %v\n", err)
		return 1
	}

	// Validate the mapping before touching the database.
	f, err := os.Open(*mappingPath)
	if err != nil {
		return fail(err)
	}
	pairs, err := useridmigration.ParseMapping(f)
	_ = f.Close()
	if err != nil {
		return fail(fmt.Errorf("mapping %s: %w", *mappingPath, err))
	}

	dsn := *dsnFlag
	if dsn == "" {
		if dsn, err = dsnFromConfig(*configPath); err != nil {
			return fail(err)
		}
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return fail(fmt.Errorf("open database: %w", err))
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close()
	}

	// An interrupt cancels the transaction, which then rolls back.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	report, err := useridmigration.Run(ctx, db, pairs, useridmigration.Options{
		Apply:       *apply,
		Reverse:     *reverse,
		LockTimeout: *lockTimeout,
	})
	if err != nil {
		if *apply {
			// A failure during COMMIT (or an interrupt racing it) leaves the
			// outcome unknown; a dry-run shows the current state safely.
			fmt.Fprintln(stderr, "migrate-user-ids: if unsure whether the changes were committed, run again without --apply to see the current state")
		}
		return fail(err)
	}
	if err := report.WriteText(stdout); err != nil {
		if *apply {
			return fail(fmt.Errorf("changes committed, but writing the report failed: %w", err))
		}
		return fail(err)
	}
	return 0
}

// dsnFromConfig reads only the database: block of the backend config file.
// It does not call internal.LoadConfig, which also validates chains and auth
// settings the tool does not need.
func dsnFromConfig(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	var cfg struct {
		Database internal.DatabaseConfig `yaml:"database"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Database.Host == "" {
		return "", fmt.Errorf("config %s: database.host is empty; pass --dsn or fix the database: block", path)
	}
	return cfg.Database.DSN(), nil
}
