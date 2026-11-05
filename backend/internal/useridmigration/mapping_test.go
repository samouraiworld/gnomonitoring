package useridmigration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const subA = "11111111-1111-4111-8111-111111111111"
const subB = "22222222-2222-4222-8222-222222222222"

func TestParseMapping_Valid(t *testing.T) {
	pairs, err := ParseMapping(strings.NewReader("clerk_user_id,sub\nuser_2alice," + subA + "\n user_2bob , " + subB + "\n"))
	require.NoError(t, err)
	assert.Equal(t, []Pair{{"user_2alice", subA}, {"user_2bob", subB}}, pairs)
}

func TestParseMapping_AcceptsCRLFBOMAndTrailingNewline(t *testing.T) {
	pairs, err := ParseMapping(strings.NewReader("\ufeffclerk_user_id,sub\r\nuser_2alice," + subA + "\r\n\r\n"))
	require.NoError(t, err)
	assert.Equal(t, []Pair{{"user_2alice", subA}}, pairs)
}

func TestParseMapping_RejectsDuplicateClerkID(t *testing.T) {
	_, err := ParseMapping(strings.NewReader("clerk_user_id,sub\nuser_2alice," + subA + "\nuser_2alice," + subB + "\n"))
	require.ErrorContains(t, err, "duplicate clerk_user_id")
	assert.ErrorContains(t, err, "line 3")
	assert.ErrorContains(t, err, "line 2")
}

func TestParseMapping_RejectsDuplicateSub(t *testing.T) {
	_, err := ParseMapping(strings.NewReader("clerk_user_id,sub\nuser_2alice," + subA + "\nuser_2bob," + subA + "\n"))
	require.ErrorContains(t, err, "duplicate sub")
	assert.ErrorContains(t, err, "line 3")
	assert.ErrorContains(t, err, "line 2")
}

func TestParseMapping_RejectsSwappedColumns(t *testing.T) {
	_, err := ParseMapping(strings.NewReader("clerk_user_id,sub\n" + subA + ",user_2alice\n"))
	assert.ErrorContains(t, err, "line 2")
}

func TestParseMapping_RejectsMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"no header":       "user_2alice," + subA + "\n",
		"wrong header":    "sub,clerk_user_id\nuser_2alice," + subA + "\n",
		"one field":       "clerk_user_id,sub\nuser_2alice\n",
		"three fields":    "clerk_user_id,sub\nuser_2alice," + subA + ",x\n",
		"empty clerk id":  "clerk_user_id,sub\n," + subA + "\n",
		"empty sub":       "clerk_user_id,sub\nuser_2alice,\n",
		"clerk id format": "clerk_user_id,sub\nalice," + subA + "\n",
		"sub not a uuid":  "clerk_user_id,sub\nuser_2alice,not-a-uuid\n",
		"uppercase uuid":  "clerk_user_id,sub\nuser_2alice,AAAAAAAA-1111-4111-8111-11111111111F\n",
		"header only":     "clerk_user_id,sub\n",
		"empty file":      "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseMapping(strings.NewReader(body))
			assert.Error(t, err)
		})
	}
}

// Every error except the empty-mapping one starts with "line N:" so the
// operator can find the bad record without the tool printing its values.
func TestParseMapping_ErrorsStartWithLineNumber(t *testing.T) {
	for name, body := range map[string]string{
		"wrong header":   "sub,clerk_user_id\nuser_2alice," + subA + "\n",
		"one field":      "clerk_user_id,sub\nuser_2alice\n",
		"sub not a uuid": "clerk_user_id,sub\nuser_2alice,not-a-uuid\n",
		"bare quote":     "clerk_user_id,sub\nuser_2\"alice," + subA + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseMapping(strings.NewReader(body))
			require.Error(t, err)
			assert.Regexp(t, `^line \d+: `, err.Error())
		})
	}
}

// Error messages carry line numbers only, never mapping values: the mapping
// holds personal identifiers and stderr may end up in a shared log.
func TestParseMapping_ErrorsDoNotEchoValues(t *testing.T) {
	_, err := ParseMapping(strings.NewReader("clerk_user_id,sub\nuser_2alice," + subA + "\nuser_2bob," + subA + "\n"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), subA)
	assert.NotContains(t, err.Error(), "user_2bob")
}

func TestErasureDigest_MatchesAccountErasureConstruction(t *testing.T) {
	assert.Equal(t, "393679df25746cca0a4524f9b010f7104c23978db734e129bff328d1d3e0c757", ErasureDigest("user_2abc"))
}
