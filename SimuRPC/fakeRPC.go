package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

var height = 1

// Liste fixe de validateurs (adresses bidon)
var validators = []map[string]interface{}{
	{"address": "VAL1", "pub_key": map[string]string{"@type": "/tm.PubKeyEd25519", "value": "KEY1"}, "voting_power": "10", "proposer_priority": "0"},
	{"address": "VAL2", "pub_key": map[string]string{"@type": "/tm.PubKeyEd25519", "value": "KEY2"}, "voting_power": "10", "proposer_priority": "0"},
	{"address": "VAL3", "pub_key": map[string]string{"@type": "/tm.PubKeyEd25519", "value": "KEY3"}, "voting_power": "10", "proposer_priority": "0"},
	{"address": "VAL4", "pub_key": map[string]string{"@type": "/tm.PubKeyEd25519", "value": "KEY4"}, "voting_power": "10", "proposer_priority": "0"},
}

func getActiveValidators(h int) []map[string]interface{} {
	switch {
	case h < 10:
		return validators[:3]
	case h < 50:
		return validators[:4]
	default:
		return []map[string]interface{}{validators[0], validators[2], validators[3]}
	}
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "",
		"result": map[string]interface{}{
			"node_info": map[string]interface{}{
				"protocol_version": map[string]string{
					"p2p":   "7",
					"block": "10",
					"app":   "1",
				},
				"id":       "gnocore-rpc-01",
				"network":  "test7.2",
				"version":  "v1.0.0-rc.0",
				"channels": "QCAhIiMwUA==",
				"moniker":  "gnocore-rpc-01",
				"other": map[string]string{
					"tx_index":    "off",
					"rpc_address": "tcp://0.0.0.0:26657",
				},
			},
			"sync_info": map[string]interface{}{
				"latest_block_hash":   "Fax1eMseoypDYWysBSrTecQDTSBWddKCWY9QlFSLQ4U=",
				"latest_app_hash":     "yGFbf3fzJTJ4pVN+ytn7M9v1mylALUVYi5b7M6LNWow=",
				"latest_block_height": strconv.Itoa(height),
				"latest_block_time":   "2025-08-11T15:51:21.304055540Z",
				"catching_up":         false,
			},
			"validator_info": map[string]interface{}{
				"address": "g1rj8ffcr9mxykxkf6jpy4250pjhzdfyjqs0umv6",
				"pub_key": map[string]string{
					"@type": "/tm.PubKeyEd25519",
					"value": "meqrzc6No5IxNrVtGqydATG8+3Vm7Qbyx+QHrDLiCuI=",
				},
				"voting_power": "0",
			},
		},
	})
}

func handleBlock(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	activeVals := getActiveValidators(height)
	precommits := make([]map[string]string, len(activeVals))
	for i, v := range activeVals {
		precommits[i] = map[string]string{"validator_address": v["address"].(string)}
	}
	height++ // incrément du bloc

	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]interface{}{
			"block": map[string]interface{}{
				"last_commit": map[string]interface{}{
					"precommits": precommits,
				},
			},
		},
	})
}

func handleValidators(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	activeVals := getActiveValidators(height) // ✅ correction ici
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]interface{}{
			"block_height": strconv.Itoa(height),
			"validators":   activeVals,
		},
	})
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
}

func main() {
	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/block", handleBlock)
	http.HandleFunc("/validators", handleValidators)
	http.HandleFunc("/", handleNotFound)

	println("🚀 Fake RPC Gnoland running on http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}
