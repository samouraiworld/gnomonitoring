package useridmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
)

// Tables lists every table that stores a gnomonitoring user id, always in a
// user_id column. Run refuses to work on a schema whose user_id tables differ
// from this list, so a new table cannot be left behind silently.
var Tables = []string{"users", "hour_reports", "alert_contacts", "webhook_gov_daos", "webhook_validators"}

// ErasureTable holds account-erasure markers keyed by ErasureDigest(user id)
// (#147). Run rewrites the digests of mapped users when the table exists.
const ErasureTable = "account_erasures"

const defaultLockTimeout = 10 * time.Second

// Options selects what Run does.
type Options struct {
	Apply       bool          // false: dry-run, the same transaction rolled back
	Reverse     bool          // true: sub -> clerk_user_id
	LockTimeout time.Duration // 0 means 10s
}

// Status is the outcome of one mapped user.
type Status string

const (
	StatusMigrated        Status = "migrated"         // rows moved from From to To
	StatusAlreadyMigrated Status = "already-migrated" // no rows under From, some under To
	StatusNoRows          Status = "no-rows"          // no rows under either id
	StatusConflict        Status = "conflict"         // rows (or erasure markers) under both ids; nothing moved
)

// UserResult reports one mapped user. From and To follow the direction.
type UserResult struct {
	From, To       string
	Status         Status
	Moved          map[string]int64 // table -> rows moved; ErasureTable -> 1 when a digest was rewritten
	ConflictTables []string         // on conflict: tables where To already has rows (ErasureTable: To's marker)
}

// Unmapped reports a stored user id that is neither a mapped Clerk id nor a
// mapped sub. Run leaves its rows alone.
type Unmapped struct {
	UserID    string
	ClerkLike bool             // has the "user_" prefix
	Rows      map[string]int64 // table -> rows
}

// Report is what Run did, or would do in a dry-run. A dry-run and an apply on
// the same data produce equal reports except for Applied.
type Report struct {
	Applied, Reverse bool
	Pairs            int
	MovedPerTable    map[string]int64 // every table in Tables, plus ErasureTable when present
	Users            []UserResult     // sorted by From
	Unmapped         []Unmapped       // sorted by UserID
	ErasureTable     bool             // account_erasures exists
}

// errDryRun makes the transaction roll back at the end of a dry-run.
var errDryRun = errors.New("dry-run: rolling back")

// Run rewrites user_id in every table of Tables, and the account-erasure
// digests when ErasureTable exists, for each pair, in one transaction.
//
// It locks the tables against writers first and fails if the locks are not
// granted within LockTimeout. A user whose old and new ids both own rows (in
// any table, or an erasure marker) is reported as a conflict and left
// untouched. Ids that own
// rows but are not in the mapping are reported and left untouched. Without
// Apply, Run performs every update and then rolls back, so the report shows
// exactly what an apply would do.
func Run(ctx context.Context, db *gorm.DB, pairs []Pair, opts Options) (*Report, error) {
	if err := checkPairs(pairs); err != nil {
		return nil, err
	}
	timeout := opts.LockTimeout
	if timeout == 0 {
		timeout = defaultLockTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("lock timeout must be positive, got %s", timeout)
	}
	timeoutMS := max(timeout.Milliseconds(), 1) // 0ms would disable the timeout

	ordered := make([]Pair, len(pairs))
	copy(ordered, pairs)
	from := func(p Pair) string {
		if opts.Reverse {
			return p.Sub
		}
		return p.ClerkUserID
	}
	sort.Slice(ordered, func(i, j int) bool { return from(ordered[i]) < from(ordered[j]) })

	report := &Report{
		Applied:       opts.Apply,
		Reverse:       opts.Reverse,
		Pairs:         len(pairs),
		MovedPerTable: map[string]int64{},
	}
	for _, table := range Tables {
		report.MovedPerTable[table] = 0
	}

	// READ COMMITTED whatever the database default: under REPEATABLE READ the
	// snapshot would be taken by the first SELECT, before LOCK TABLE, and the
	// counts and updates would miss rows written while the lock was awaited.
	txOpts := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('lock_timeout', ?, true)", fmt.Sprintf("%dms", timeoutMS)).Error; err != nil {
			return fmt.Errorf("set lock_timeout: %w", err)
		}
		hasErasure, err := checkSchema(tx)
		if err != nil {
			return err
		}
		report.ErasureTable = hasErasure

		locked := append([]string{}, Tables...)
		if hasErasure {
			locked = append(locked, ErasureTable)
			report.MovedPerTable[ErasureTable] = 0
		}
		if err := tx.Exec("LOCK TABLE " + strings.Join(locked, ", ") + " IN EXCLUSIVE MODE").Error; err != nil {
			return fmt.Errorf("lock tables: %w", err)
		}

		counts, err := countRowsPerUser(tx)
		if err != nil {
			return err
		}
		digests := map[string]bool{}
		if hasErasure {
			var list []string
			if err := tx.Raw("SELECT digest FROM " + ErasureTable).Scan(&list).Error; err != nil {
				return fmt.Errorf("read %s: %w", ErasureTable, err)
			}
			for _, d := range list {
				digests[d] = true
			}
		}

		for _, p := range ordered {
			src, dst := p.ClerkUserID, p.Sub
			if opts.Reverse {
				src, dst = dst, src
			}
			res, err := migrateUser(tx, src, dst, counts, hasErasure, digests)
			if err != nil {
				return err
			}
			for table, n := range res.Moved {
				report.MovedPerTable[table] += n
			}
			report.Users = append(report.Users, res)
		}

		report.Unmapped = findUnmapped(pairs, counts)

		if !opts.Apply {
			return errDryRun
		}
		return nil
	}, txOpts)
	if err != nil && !errors.Is(err, errDryRun) {
		return nil, err
	}
	return report, nil
}

// checkPairs repeats the mapping checks of ParseMapping, so Run never merges
// two people because of a caller that skipped them.
func checkPairs(pairs []Pair) error {
	if len(pairs) == 0 {
		return errEmptyMapping
	}
	seenClerk := map[string]bool{}
	seenSub := map[string]bool{}
	for i, p := range pairs {
		if !clerkUserIDPattern.MatchString(p.ClerkUserID) || !subPattern.MatchString(p.Sub) {
			return fmt.Errorf("pair %d: not a Clerk user id and a lowercase UUID sub", i+1)
		}
		if seenClerk[p.ClerkUserID] {
			return fmt.Errorf("pair %d: duplicate clerk_user_id", i+1)
		}
		if seenSub[p.Sub] {
			return fmt.Errorf("pair %d: duplicate sub", i+1)
		}
		seenClerk[p.ClerkUserID] = true
		seenSub[p.Sub] = true
	}
	return nil
}

// checkSchema verifies that the base tables with a user_id column in the
// current schema are exactly Tables, and reports whether ErasureTable exists.
func checkSchema(tx *gorm.DB) (bool, error) {
	var found []string
	err := tx.Raw(`SELECT c.table_name
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = current_schema()
		  AND c.column_name = 'user_id'
		  AND t.table_type = 'BASE TABLE'
		ORDER BY c.table_name`).Scan(&found).Error
	if err != nil {
		return false, fmt.Errorf("list user_id tables: %w", err)
	}
	known := map[string]bool{}
	for _, table := range Tables {
		known[table] = true
	}
	present := map[string]bool{}
	var unknown []string
	for _, table := range found {
		present[table] = true
		if !known[table] {
			unknown = append(unknown, table)
		}
	}
	if len(unknown) > 0 {
		return false, fmt.Errorf("refusing to run: table(s) %s also have a user_id column; the tool only knows %s",
			strings.Join(unknown, ", "), strings.Join(Tables, ", "))
	}
	var missing []string
	for _, table := range Tables {
		if !present[table] {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Errorf("refusing to run: expected table(s) %s with a user_id column not found in the current schema",
			strings.Join(missing, ", "))
	}

	var erasure int64
	err = tx.Raw(`SELECT count(*) FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = ? AND table_type = 'BASE TABLE'`, ErasureTable).Scan(&erasure).Error
	if err != nil {
		return false, fmt.Errorf("look up %s: %w", ErasureTable, err)
	}
	return erasure > 0, nil
}

// countRowsPerUser returns, for every table of Tables, the row count of each
// stored user id. The tables are locked, so the counts hold until commit.
func countRowsPerUser(tx *gorm.DB) (map[string]map[string]int64, error) {
	counts := map[string]map[string]int64{}
	for _, table := range Tables {
		var rows []struct {
			UserID string
			N      int64
		}
		if err := tx.Raw("SELECT user_id, count(*) AS n FROM " + table + " GROUP BY user_id").Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		counts[table] = map[string]int64{}
		for _, r := range rows {
			counts[table][r.UserID] = r.N
		}
	}
	return counts, nil
}

// migrateUser moves one user's rows from src to dst, or reports why not.
func migrateUser(tx *gorm.DB, src, dst string, counts map[string]map[string]int64, hasErasure bool, digests map[string]bool) (UserResult, error) {
	res := UserResult{From: src, To: dst}
	srcRows := map[string]int64{}
	var dstTables []string
	for _, table := range Tables {
		if n := counts[table][src]; n > 0 {
			srcRows[table] = n
		}
		if counts[table][dst] > 0 {
			dstTables = append(dstTables, table)
		}
	}
	srcDigest, dstDigest := ErasureDigest(src), ErasureDigest(dst)
	srcMarker := hasErasure && digests[srcDigest]
	if hasErasure && digests[dstDigest] {
		dstTables = append(dstTables, ErasureTable)
	}
	srcHasData := len(srcRows) > 0 || srcMarker

	// Any data under both ids, in the same table or not, erasure markers
	// included, is a conflict: the user is left whole for a manual merge, so
	// a wrong pair can never fold one person's rows into another's.
	switch {
	case srcHasData && len(dstTables) > 0:
		res.Status = StatusConflict
		res.ConflictTables = dstTables
		return res, nil
	case !srcHasData:
		if len(dstTables) > 0 {
			res.Status = StatusAlreadyMigrated
		} else {
			res.Status = StatusNoRows
		}
		return res, nil
	}

	res.Status = StatusMigrated
	res.Moved = map[string]int64{}
	for _, table := range Tables {
		want := srcRows[table]
		if want == 0 {
			continue
		}
		result := tx.Exec("UPDATE "+table+" SET user_id = ? WHERE user_id = ?", dst, src)
		if result.Error != nil {
			return res, fmt.Errorf("update %s: %w", table, result.Error)
		}
		if result.RowsAffected != want {
			return res, fmt.Errorf("update %s: counted %d rows, updated %d", table, want, result.RowsAffected)
		}
		res.Moved[table] = result.RowsAffected
	}
	if srcMarker {
		result := tx.Exec("UPDATE "+ErasureTable+" SET digest = ? WHERE digest = ?", dstDigest, srcDigest)
		if result.Error != nil {
			return res, fmt.Errorf("update %s: %w", ErasureTable, result.Error)
		}
		if result.RowsAffected != 1 {
			return res, fmt.Errorf("update %s: expected 1 digest, updated %d", ErasureTable, result.RowsAffected)
		}
		res.Moved[ErasureTable] = 1
	}
	return res, nil
}

// findUnmapped lists the stored user ids that are neither a mapped Clerk id
// nor a mapped sub, from counts taken before any update.
func findUnmapped(pairs []Pair, counts map[string]map[string]int64) []Unmapped {
	mapped := map[string]bool{}
	for _, p := range pairs {
		mapped[p.ClerkUserID] = true
		mapped[p.Sub] = true
	}
	byID := map[string]*Unmapped{}
	for _, table := range Tables {
		for id, n := range counts[table] {
			if mapped[id] || n == 0 {
				continue
			}
			u, ok := byID[id]
			if !ok {
				u = &Unmapped{UserID: id, ClerkLike: strings.HasPrefix(id, "user_"), Rows: map[string]int64{}}
				byID[id] = u
			}
			u.Rows[table] = n
		}
	}
	unmapped := make([]Unmapped, 0, len(byID))
	for _, u := range byID {
		unmapped = append(unmapped, *u)
	}
	sort.Slice(unmapped, func(i, j int) bool { return unmapped[i].UserID < unmapped[j].UserID })
	return unmapped
}

// WriteText prints the report in the line format the operator runbook greps.
// It prints user ids and counts only, never emails or webhook URLs. The
// MIGRATED lines repeat the mapping: treat the report like the mapping.
func (r *Report) WriteText(w io.Writer) error {
	p := &textWriter{w: w}
	mode, direction := "dry-run", "forward"
	if r.Applied {
		mode = "apply"
	}
	if r.Reverse {
		direction = "reverse"
	}
	p.printf("migrate-user-ids: mode=%s direction=%s pairs=%d\n", mode, direction, r.Pairs)
	for _, table := range Tables {
		p.printf("table %s moved=%d\n", table, r.MovedPerTable[table])
	}
	if r.ErasureTable {
		p.printf("table %s moved=%d\n", ErasureTable, r.MovedPerTable[ErasureTable])
	} else {
		p.printf("table %s absent\n", ErasureTable)
	}

	byStatus := map[Status]int{}
	for _, u := range r.Users {
		byStatus[u.Status]++
	}
	p.printf("users migrated=%d already-migrated=%d no-rows=%d conflict=%d unmapped=%d\n",
		byStatus[StatusMigrated], byStatus[StatusAlreadyMigrated], byStatus[StatusNoRows], byStatus[StatusConflict], len(r.Unmapped))

	allTables := append(append([]string{}, Tables...), ErasureTable)
	for _, u := range r.Users {
		if u.Status != StatusMigrated {
			continue
		}
		p.printf("MIGRATED %s -> %s:", printableID(u.From), printableID(u.To))
		for _, table := range allTables {
			if n := u.Moved[table]; n > 0 {
				p.printf(" %s=%d", table, n)
			}
		}
		p.printf("\n")
	}
	for _, u := range r.Users {
		if u.Status == StatusConflict {
			p.printf("CONFLICT %s -> %s: %s\n", printableID(u.From), printableID(u.To), strings.Join(u.ConflictTables, " "))
		}
	}
	for _, u := range r.Unmapped {
		p.printf("UNMAPPED %s clerk-like=%t:", printableID(u.UserID), u.ClerkLike)
		for _, table := range Tables {
			if n := u.Rows[table]; n > 0 {
				p.printf(" %s=%d", table, n)
			}
		}
		p.printf("\n")
	}
	if r.Applied {
		p.printf("result: committed\n")
	} else {
		p.printf("result: rolled back (dry-run)\n")
	}
	return p.err
}

// textWriter keeps the first write error so WriteText checks it once.
type textWriter struct {
	w   io.Writer
	err error
}

func (p *textWriter) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

// printableID returns id as is when it holds only printable, non-space
// characters, and Go-quoted otherwise, so a stored id cannot forge report
// lines or send terminal escapes.
func printableID(id string) string {
	if id == "" {
		return `""`
	}
	for _, r := range id {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return strconv.Quote(id)
		}
	}
	return id
}
