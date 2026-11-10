// Package useridmigration rewrites the user ids gnomonitoring stores, from
// Clerk user ids to Keycloak subjects (sub), or back.
//
// It backs the one-shot command backend/cmd/migrate-user-ids. The command
// runs with the backend stopped, inside one database transaction, and is a
// dry-run unless the operator asks to apply. The mapping it reads pairs each
// Clerk user id with its Keycloak sub; it holds personal identifiers and is
// never committed, pasted into an issue or a pull request, or logged. The
// report repeats those ids (never emails or URLs) and is handled the same way.
package useridmigration

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Pair maps one Clerk user id to the Keycloak sub of the same person.
type Pair struct {
	ClerkUserID, Sub string
}

var (
	clerkUserIDPattern = regexp.MustCompile(`^user_[A-Za-z0-9]+$`)
	subPattern         = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	utf8BOM            = []byte{0xEF, 0xBB, 0xBF}
)

// mappingHeader is the required first record of the mapping file.
var mappingHeader = [2]string{"clerk_user_id", "sub"}

// errEmptyMapping is the only parse error without a line number.
var errEmptyMapping = errors.New("mapping is empty: it needs the header clerk_user_id,sub and at least one data row")

// ParseMapping reads a CSV mapping with the header clerk_user_id,sub.
//
// It accepts a leading UTF-8 byte order mark, CRLF line endings and blank
// lines. Values are trimmed. A Clerk id must look like user_<alphanumeric>
// and a sub must be a lowercase UUID, so swapped columns are refused. A
// duplicate Clerk id or sub is refused. Errors name line numbers (the header
// is line 1) but never the values, which are personal identifiers.
func ParseMapping(r io.Reader) ([]Pair, error) {
	br := bufio.NewReader(r)
	if head, err := br.Peek(len(utf8BOM)); err == nil && bytes.Equal(head, utf8BOM) {
		if _, err := br.Discard(len(utf8BOM)); err != nil {
			return nil, err
		}
	}

	cr := csv.NewReader(br)
	cr.FieldsPerRecord = 2

	var (
		pairs     []Pair
		seenClerk = map[string]int{}
		seenSub   = map[string]int{}
		gotHeader bool
	)
	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				return nil, fmt.Errorf("line %d: %w", pe.Line, pe.Err)
			}
			return nil, err
		}
		line, _ := cr.FieldPos(0)

		if !gotHeader {
			if strings.TrimSpace(record[0]) != mappingHeader[0] || strings.TrimSpace(record[1]) != mappingHeader[1] {
				return nil, fmt.Errorf("line %d: the header must be %s,%s", line, mappingHeader[0], mappingHeader[1])
			}
			gotHeader = true
			continue
		}

		clerkID := strings.TrimSpace(record[0])
		sub := strings.TrimSpace(record[1])
		switch {
		case clerkID == "":
			return nil, fmt.Errorf("line %d: clerk_user_id is empty", line)
		case sub == "":
			return nil, fmt.Errorf("line %d: sub is empty", line)
		case !clerkUserIDPattern.MatchString(clerkID):
			return nil, fmt.Errorf("line %d: clerk_user_id is not of the form user_<letters and digits> (columns swapped?)", line)
		case !subPattern.MatchString(sub):
			return nil, fmt.Errorf("line %d: sub is not a lowercase UUID (columns swapped?)", line)
		}
		if first, ok := seenClerk[clerkID]; ok {
			return nil, fmt.Errorf("line %d: duplicate clerk_user_id, first seen on line %d", line, first)
		}
		if first, ok := seenSub[sub]; ok {
			return nil, fmt.Errorf("line %d: duplicate sub, first seen on line %d", line, first)
		}
		seenClerk[clerkID] = line
		seenSub[sub] = line
		pairs = append(pairs, Pair{ClerkUserID: clerkID, Sub: sub})
	}
	if len(pairs) == 0 {
		return nil, errEmptyMapping
	}
	return pairs, nil
}

// ErasureDigest returns the account-erasure digest of a user id: the
// lowercase hex SHA-256 of "gnomonitoring:account-erasure:v1\x00" + userID,
// the construction of account_erasures.digest (accountErasureDigest, #147).
func ErasureDigest(userID string) string {
	sum := sha256.Sum256([]byte("gnomonitoring:account-erasure:v1\x00" + userID))
	return hex.EncodeToString(sum[:])
}
