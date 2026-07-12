package gnovalidator

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// sortEvents gives deterministic ordering for assertions, independent of the
// map-iteration order inside classifyValsetChanges.
func sortEvents(events []ValsetChangeEvent) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].Kind != events[j].Kind {
			return events[i].Kind < events[j].Kind
		}
		return events[i].OldAddr+events[i].NewAddr < events[j].OldAddr+events[j].NewAddr
	})
}

func TestClassifyValsetChanges_MatchedRotation(t *testing.T) {
	oldMap := map[string]string{"g1old": "Validaria"}
	newMap := map[string]string{"g1new": "Validaria"}
	prevSigningToOperator := map[string]string{"g1old": "g1operator"}
	currentValopers := []Valoper{
		{Name: "Validaria", Address: "g1operator", SigningAddress: "g1new"},
	}

	events := classifyValsetChanges(oldMap, newMap, prevSigningToOperator, currentValopers)
	require.Len(t, events, 1)
	require.Equal(t, ValidatorAddressChanged, events[0].Kind)
	require.Equal(t, "g1old", events[0].OldAddr)
	require.Equal(t, "g1new", events[0].NewAddr)
	require.Equal(t, "Validaria", events[0].Moniker)
}

func TestClassifyValsetChanges_UnmatchedDeparture(t *testing.T) {
	oldMap := map[string]string{"g1gone": "Ghostly"}
	newMap := map[string]string{}
	// No valoper profile ties g1gone to any operator that rotated.
	events := classifyValsetChanges(oldMap, newMap, map[string]string{}, nil)
	require.Len(t, events, 1)
	require.Equal(t, ValidatorLeft, events[0].Kind)
	require.Equal(t, "g1gone", events[0].OldAddr)
	require.Equal(t, "Ghostly", events[0].Moniker)
}

func TestClassifyValsetChanges_UnmatchedArrival(t *testing.T) {
	oldMap := map[string]string{}
	newMap := map[string]string{"g1fresh": "Freshling"}
	events := classifyValsetChanges(oldMap, newMap, map[string]string{}, nil)
	require.Len(t, events, 1)
	require.Equal(t, ValidatorJoined, events[0].Kind)
	require.Equal(t, "g1fresh", events[0].NewAddr)
	require.Equal(t, "Freshling", events[0].Moniker)
}

func TestClassifyValsetChanges_UnrelatedDepartureAndArrival(t *testing.T) {
	oldMap := map[string]string{"g1left": "Leaver"}
	newMap := map[string]string{"g1joined": "Joiner"}
	// g1left's operator either isn't found, or its current signing address
	// isn't g1joined — no rotation match should be made.
	prevSigningToOperator := map[string]string{"g1left": "g1operatorA"}
	currentValopers := []Valoper{
		{Name: "Leaver", Address: "g1operatorA", SigningAddress: "g1left"}, // did not rotate
		{Name: "Joiner", Address: "g1operatorB", SigningAddress: "g1joined"},
	}

	events := classifyValsetChanges(oldMap, newMap, prevSigningToOperator, currentValopers)
	sortEvents(events)
	require.Len(t, events, 2)
	require.Equal(t, ValidatorLeft, events[0].Kind)
	require.Equal(t, "g1left", events[0].OldAddr)
	require.Equal(t, ValidatorJoined, events[1].Kind)
	require.Equal(t, "g1joined", events[1].NewAddr)
}

func TestSigningToOperatorFromValopers(t *testing.T) {
	got := signingToOperatorFromValopers([]Valoper{
		{Name: "A", Address: "g1opA", SigningAddress: "g1signA"},
		{Name: "B", Address: "g1opB", SigningAddress: ""}, // no signing address declared yet — skipped
	})
	require.Equal(t, map[string]string{"g1signA": "g1opA"}, got)
}

func TestSigningToOperatorState(t *testing.T) {
	chainID := "test-signing-to-operator-state"
	require.Empty(t, getSigningToOperator(chainID))

	setSigningToOperator(chainID, map[string]string{"g1sign": "g1op"})
	require.Equal(t, map[string]string{"g1sign": "g1op"}, getSigningToOperator(chainID))
}
