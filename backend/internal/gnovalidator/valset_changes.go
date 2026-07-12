package gnovalidator

import "sync"

// ValsetChangeKind classifies one detected valset membership change.
type ValsetChangeKind int

const (
	// Declared in this order (rather than matching the Joined/Left/AddressChanged
	// prose order used elsewhere) so that ascending-by-Kind sorts — such as the
	// test helper sortEvents in valset_changes_test.go — naturally place
	// departures before arrivals. The actual scenario each constant represents
	// is identified by name everywhere in this codebase; only the ordinal
	// values matter for that sort, and no other code depends on them.
	ValidatorLeft ValsetChangeKind = iota
	ValidatorJoined
	ValidatorAddressChanged
)

// ValsetChangeEvent describes one detected change between two consecutive
// MonikerMap snapshots, already correlated against the valoper registry to
// distinguish a signing-key rotation from an unrelated departure/arrival.
type ValsetChangeEvent struct {
	Kind    ValsetChangeKind
	Moniker string
	OldAddr string // set for ValidatorLeft and ValidatorAddressChanged
	NewAddr string // set for ValidatorJoined and ValidatorAddressChanged
}

// signingToOperatorMap[chainID][signingAddr] = operatorAddr, as observed the
// last time classifyValsetChanges ran for that chain. Mirrors the
// MonikerMap/FirstActiveBlockMap package-level state pattern.
var signingToOperatorMap = make(map[string]map[string]string)
var signingToOperatorMutex sync.RWMutex

// getSigningToOperator returns a snapshot of the previous cycle's
// signing-address -> operator-address map for chainID (empty if never set).
func getSigningToOperator(chainID string) map[string]string {
	signingToOperatorMutex.RLock()
	defer signingToOperatorMutex.RUnlock()
	m, ok := signingToOperatorMap[chainID]
	if !ok {
		return make(map[string]string)
	}
	snapshot := make(map[string]string, len(m))
	for k, v := range m {
		snapshot[k] = v
	}
	return snapshot
}

// setSigningToOperator replaces the per-chain signing-address -> operator
// map, to be read back by the next cycle's classifyValsetChanges call.
func setSigningToOperator(chainID string, m map[string]string) {
	signingToOperatorMutex.Lock()
	defer signingToOperatorMutex.Unlock()
	signingToOperatorMap[chainID] = m
}

// signingToOperatorFromValopers builds a signing-address -> operator-address
// map from a valoper registry snapshot (skipping profiles with no declared
// signing address), for use as the next cycle's prevSigningToOperator input
// to classifyValsetChanges.
func signingToOperatorFromValopers(valopers []Valoper) map[string]string {
	m := make(map[string]string, len(valopers))
	for _, v := range valopers {
		if v.SigningAddress != "" {
			m[v.SigningAddress] = v.Address
		}
	}
	return m
}

// classifyValsetChanges compares oldMap (the moniker snapshot before this
// refresh cycle) with newMap (the snapshot after), and correlates any
// departures with arrivals via prevSigningToOperator (each now-removed
// signing address's operator, as observed in the PREVIOUS cycle) and
// currentValopers (this cycle's freshly fetched valoper registry, giving
// each operator's up-to-date declared signing address).
//
// A departed address whose operator's current signing address is one of
// this cycle's new arrivals is reported as a single ValidatorAddressChanged
// event instead of a separate ValidatorLeft + ValidatorJoined pair.
func classifyValsetChanges(
	oldMap, newMap map[string]string,
	prevSigningToOperator map[string]string,
	currentValopers []Valoper,
) []ValsetChangeEvent {
	removed := make(map[string]bool)
	for addr := range oldMap {
		if _, ok := newMap[addr]; !ok {
			removed[addr] = true
		}
	}
	added := make(map[string]bool)
	for addr := range newMap {
		if _, ok := oldMap[addr]; !ok {
			added[addr] = true
		}
	}

	operatorCurrentSigning := make(map[string]string, len(currentValopers))
	for _, v := range currentValopers {
		if v.SigningAddress != "" {
			operatorCurrentSigning[v.Address] = v.SigningAddress
		}
	}

	var events []ValsetChangeEvent
	matchedArrival := make(map[string]bool)

	for r := range removed {
		if operator, ok := prevSigningToOperator[r]; ok {
			if newSigning, ok := operatorCurrentSigning[operator]; ok && newSigning != r && added[newSigning] {
				events = append(events, ValsetChangeEvent{
					Kind:    ValidatorAddressChanged,
					Moniker: oldMap[r],
					OldAddr: r,
					NewAddr: newSigning,
				})
				matchedArrival[newSigning] = true
				continue
			}
		}
		events = append(events, ValsetChangeEvent{
			Kind:    ValidatorLeft,
			Moniker: oldMap[r],
			OldAddr: r,
		})
	}

	for a := range added {
		if matchedArrival[a] {
			continue
		}
		events = append(events, ValsetChangeEvent{
			Kind:    ValidatorJoined,
			Moniker: newMap[a],
			NewAddr: a,
		})
	}

	return events
}
