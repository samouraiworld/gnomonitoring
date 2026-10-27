// rehearse-initdb invokes only database.InitDB against an explicitly guarded
// local clone. It never loads application config, starts collectors or bots,
// restores a dump, creates a marker, or resets a schema.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

const expectedDatabaseSourceSHA = "b89ab62f218255680065eaca18db81bfe86c53ded9667bb4c839e59eeb43eff9"

// The fixture Dockerfile computes this from the source it compiles. A normal
// unpinned go run/go build is intentionally insufficient to mutate a clone.
var databaseSourceSHA = "unrecorded"

type tableCount struct {
	Present            bool         `json:"present"`
	Rows               int64        `json:"rows"`
	ChainScoped        bool         `json:"chain_scoped"`
	ChainColumnPresent bool         `json:"chain_column_present"`
	Scopes             []scopeCount `json:"scopes"`
}

type scopeCount struct {
	Scope   string  `json:"scope"`
	ChainID *string `json:"chain_id,omitempty"`
	Rows    int64   `json:"rows"`
}

type scopeReduction struct {
	Table       string  `json:"table"`
	Scope       string  `json:"scope"`
	ChainID     *string `json:"chain_id,omitempty"`
	Before      int64   `json:"before"`
	After       int64   `json:"after"`
	RowsReduced int64   `json:"rows_reduced"`
}

type result struct {
	Status            string                `json:"status"`
	ErrorClass        string                `json:"error_class,omitempty"`
	BaseRevision      string                `json:"database_base_revision"`
	DatabaseSourceSHA string                `json:"database_source_sha256"`
	Before            map[string]tableCount `json:"before,omitempty"`
	After             map[string]tableCount `json:"after,omitempty"`
	RowsRemoved       map[string]int64      `json:"rows_removed,omitempty"`
	ScopeReductions   []scopeReduction      `json:"scope_reductions,omitempty"`
	PrivateLog        string                `json:"private_log,omitempty"`
}

func main() {
	report, status := run(os.Args[1:])
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		os.Exit(1)
	}
	os.Exit(status)
}

func run(arguments []string) (result, int) {
	report := result{Status: "refused", BaseRevision: baseRevision, DatabaseSourceSHA: databaseSourceSHA}
	fail := func(class string) (result, int) { report.ErrorClass = class; return report, 1 }
	flags := flag.NewFlagSet("rehearse-initdb", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dsnPath := flags.String("clone-dsn-file", "", "required private clone URL file")
	cloneID := flags.String("clone-id", "", "required nonce from the isolated bootstrap")
	allow := flags.Bool("allow-initdb-on-isolated-clone", false, "explicitly permit clone mutation")
	if flags.Parse(arguments) != nil || flags.NArg() != 0 {
		return fail("invalid_arguments")
	}
	if !*allow || !cloneIDPattern.MatchString(*cloneID) || *dsnPath != dsnFilePath {
		return fail("explicit_isolated_clone_opt_in_required")
	}
	if databaseSourceSHA != expectedDatabaseSourceSHA {
		return fail("database_source_pin_required")
	}
	if err := rejectPostgresEnvironment(os.Environ()); err != nil {
		return fail(err.Error())
	}
	state, err := readNetworkEvidence()
	if err != nil {
		return fail(err.Error())
	}
	if err := validateNetworkFence(state); err != nil {
		return fail(err.Error())
	}
	if err := validateFilesystemFence(); err != nil {
		return fail(err.Error())
	}
	data, err := secureFile(*dsnPath, state.UID)
	if err != nil {
		return fail(err.Error())
	}
	dsn, err := canonicalCloneDSN(string(data), *cloneID)
	if err != nil {
		return fail(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fail("clone_connection_failed")
	}
	defer func() { _ = connection.Close(context.Background()) }()
	if err := verifyCloneMarker(ctx, connection, *cloneID); err != nil {
		return fail(err.Error())
	}
	report.Before, err = snapshot(ctx, connection)
	if err != nil {
		return fail("clone_snapshot_before_failed")
	}
	logFile, err := os.OpenFile(filepath.Join(privateLogDir, fmt.Sprintf("initdb-%d.log", time.Now().UnixNano())), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fail("private_capture_unavailable")
	}
	report.PrivateLog = logFile.Name()
	err = capturePrivate(logFile, func() error {
		db, initErr := database.InitDB(dsn)
		if initErr != nil {
			return errors.New("initdb_failed")
		}
		sqlDB, dbErr := db.DB()
		if dbErr != nil {
			return errors.New("initdb_pool_failed")
		}
		if sqlDB.Close() != nil {
			return errors.New("initdb_pool_close_failed")
		}
		return nil
	})
	_ = logFile.Close()
	if err != nil {
		return fail("initdb_or_private_capture_failed")
	}
	report.After, err = snapshot(ctx, connection)
	if err != nil {
		return fail("clone_snapshot_after_failed")
	}
	report.RowsRemoved = removedRows(report.Before, report.After)
	report.ScopeReductions = reducedScopes(report.Before, report.After)
	if len(report.RowsRemoved) != 0 || len(report.ScopeReductions) != 0 {
		report.Status = "completed_requires_deletion_review"
		return report, 3
	}
	report.Status = "completed"
	return report, 0
}

func verifyCloneMarker(ctx context.Context, connection *pgx.Conn, cloneID string) error {
	var dbName, role, sessionRole string
	var unixSocket, dangerousRole bool
	if err := connection.QueryRow(ctx, `SELECT current_database(), current_user, session_user, inet_server_addr() IS NULL,
 (SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname=current_user)`).Scan(&dbName, &role, &sessionRole, &unixSocket, &dangerousRole); err != nil ||
		dbName != "gnomonitoring_rehearsal_"+cloneID || role != cloneUser || sessionRole != cloneUser || !unixSocket || dangerousRole {
		return errors.New("clone_identity_refused")
	}
	var owners markerOwnership
	if err := connection.QueryRow(ctx, `SELECT schema_owner.rolname, table_owner.rolname, c.relkind::text,
 pg_has_role(current_user,schema_owner.oid,'MEMBER') OR pg_has_role(current_user,schema_owner.oid,'USAGE') OR pg_has_role(current_user,schema_owner.oid,'SET'),
 pg_has_role(current_user,table_owner.oid,'MEMBER') OR pg_has_role(current_user,table_owner.oid,'USAGE') OR pg_has_role(current_user,table_owner.oid,'SET')
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_roles schema_owner ON schema_owner.oid=n.nspowner JOIN pg_roles table_owner ON table_owner.oid=c.relowner
 WHERE n.nspname='rehearsal_guard' AND c.relname='clone_marker'`).Scan(&owners.SchemaOwner, &owners.TableOwner, &owners.RelationKind, &owners.SchemaRoleReachable, &owners.TableRoleReachable); err != nil {
		return errors.New("clone_marker_owner_unreadable")
	}
	if err := validateMarkerOwnership(owners); err != nil {
		return err
	}
	var canCreate, canWrite bool
	if err := connection.QueryRow(ctx, `SELECT has_schema_privilege(current_user,'rehearsal_guard','CREATE'),
 has_table_privilege(current_user,'rehearsal_guard.clone_marker','INSERT,UPDATE,DELETE,TRUNCATE')`).Scan(&canCreate, &canWrite); err != nil || canCreate || canWrite {
		return errors.New("clone_marker_not_sealed")
	}
	var markerID, markerDB, markerRevision string
	var rows int
	if err := connection.QueryRow(ctx, `SELECT count(*), min(clone_id), min(database_name), min(base_revision)
 FROM rehearsal_guard.clone_marker`).Scan(&rows, &markerID, &markerDB, &markerRevision); err != nil ||
		rows != 1 || markerID != cloneID || markerDB != dbName || markerRevision != baseRevision {
		return errors.New("clone_marker_missing_or_mismatched")
	}
	return nil
}

type markerOwnership struct {
	SchemaOwner, TableOwner, RelationKind   string
	SchemaRoleReachable, TableRoleReachable bool
}

func validateMarkerOwnership(owners markerOwnership) error {
	if owners.SchemaOwner != "rehearsal_admin" || owners.TableOwner != "rehearsal_admin" || owners.RelationKind != "r" || owners.SchemaRoleReachable || owners.TableRoleReachable {
		return errors.New("clone_marker_owner_not_independent")
	}
	return nil
}

func snapshot(ctx context.Context, connection *pgx.Conn) (map[string]tableCount, error) {
	counts := map[string]tableCount{}
	for _, table := range []string{"daily_participations", "daily_participation_agregas", "addr_monikers", "govdaos", "telegrams", "admin_configs", "alert_logs", "telegram_validator_subs"} {
		var exists bool
		if err := connection.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil {
			return nil, err
		}
		count := tableCount{Present: exists, ChainScoped: table != "admin_configs", Scopes: []scopeCount{}}
		if exists {
			if count.ChainScoped {
				if err := connection.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
 WHERE table_schema='public' AND table_name=$1 AND column_name='chain_id')`, table).Scan(&count.ChainColumnPresent); err != nil {
					return nil, err
				}
			}
			if count.ChainColumnPresent {
				rows, err := connection.Query(ctx, "SELECT chain_id, count(*) FROM "+pgx.Identifier{"public", table}.Sanitize()+" GROUP BY chain_id ORDER BY chain_id NULLS FIRST")
				if err != nil {
					return nil, err
				}
				for rows.Next() {
					var chainID pgtype.Text
					var total int64
					if err := rows.Scan(&chainID, &total); err != nil {
						rows.Close()
						return nil, err
					}
					count.Scopes = append(count.Scopes, chainScope(chainID, total))
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return nil, err
				}
			} else {
				scope := scopeCount{Scope: "global"}
				if count.ChainScoped {
					scope.Scope = "legacy_without_chain_id"
				}
				if err := connection.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{"public", table}.Sanitize()).Scan(&scope.Rows); err != nil {
					return nil, err
				}
				count.Scopes = append(count.Scopes, scope)
			}
			for _, scope := range count.Scopes {
				count.Rows += scope.Rows
			}
		}
		counts[table] = count
	}
	return counts, nil
}

func chainScope(chainID pgtype.Text, rows int64) scopeCount {
	if !chainID.Valid {
		return scopeCount{Scope: "null_chain_id", Rows: rows}
	}
	value := chainID.String
	return scopeCount{Scope: "chain", ChainID: &value, Rows: rows}
}

type scopeKey struct {
	Scope, ChainID string
}

func keyForScope(scope scopeCount) scopeKey {
	key := scopeKey{Scope: scope.Scope}
	if scope.ChainID != nil {
		key.ChainID = *scope.ChainID
	}
	return key
}

func reducedScopes(before, after map[string]tableCount) []scopeReduction {
	tables := make([]string, 0, len(before))
	for table := range before {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	var reductions []scopeReduction
	for _, table := range tables {
		afterRows := map[scopeKey]int64{}
		for _, scope := range after[table].Scopes {
			afterRows[keyForScope(scope)] = scope.Rows
		}
		for _, scope := range before[table].Scopes {
			remaining := afterRows[keyForScope(scope)]
			if scope.Rows > remaining {
				reductions = append(reductions, scopeReduction{Table: table, Scope: scope.Scope, ChainID: scope.ChainID, Before: scope.Rows, After: remaining, RowsReduced: scope.Rows - remaining})
			}
		}
	}
	return reductions
}

func removedRows(before, after map[string]tableCount) map[string]int64 {
	removed := map[string]int64{}
	for table, count := range before {
		if count.Rows > after[table].Rows {
			removed[table] = count.Rows - after[table].Rows
		}
	}
	return removed
}

// GORM and the database package write through loggers created before main.
// Redirect the actual descriptors, not just os.Stdout/os.Stderr variables.
// Raw SQL errors/DETAIL remain in a mode-0600 file in the private clone enclave.
func capturePrivate(file *os.File, operation func() error) (err error) {
	out, err := syscall.Dup(1)
	if err != nil {
		return errors.New("private_capture_failed")
	}
	defer func() { _ = syscall.Close(out) }()
	errFD, err := syscall.Dup(2)
	if err != nil {
		return errors.New("private_capture_failed")
	}
	defer func() { _ = syscall.Close(errFD) }()
	defer func() {
		if recover() != nil {
			err = errors.New("initdb_internal_failure")
		}
		if replaceFD(out, 1) != nil {
			err = errors.New("private_capture_restore_failed")
		}
		if replaceFD(errFD, 2) != nil {
			err = errors.New("private_capture_restore_failed")
		}
	}()
	if replaceFD(int(file.Fd()), 1) != nil || replaceFD(int(file.Fd()), 2) != nil {
		return errors.New("private_capture_failed")
	}
	err = operation()
	if file.Sync() != nil {
		return errors.New("private_capture_sync_failed")
	}
	return err
}
