package gnovalidator

import "sync"

// valsetVotingPower holds, per chain, the latest /validators voting power by
// address. It is the quorum denominator for late_for_quorum and is replaced
// (not merged) on every InitMonikerMap refresh. Kept in memory so the block
// hot path never queries the database for it.
var (
	valsetVotingPowerMu sync.RWMutex
	valsetVotingPower   = make(map[string]map[string]int64)
)

func setValsetVotingPower(chainID string, vp map[string]int64) {
	snapshot := make(map[string]int64, len(vp))
	for addr, power := range vp {
		snapshot[addr] = power
	}
	valsetVotingPowerMu.Lock()
	defer valsetVotingPowerMu.Unlock()
	valsetVotingPower[chainID] = snapshot
}

func getValsetVotingPower(chainID string) map[string]int64 {
	valsetVotingPowerMu.RLock()
	defer valsetVotingPowerMu.RUnlock()
	src := valsetVotingPower[chainID]
	out := make(map[string]int64, len(src))
	for addr, power := range src {
		out[addr] = power
	}
	return out
}
