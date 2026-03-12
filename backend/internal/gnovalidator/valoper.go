package gnovalidator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

type Valoper struct {
	Name    string
	Address string
}

func GetValopers(client gnoclient.Client) ([]Valoper, error) {
	var allValopers []Valoper
	page := 1

	for {

		cmd := fmt.Sprintf(`gno.land/r/gnops/valopers.Render("?page=%d")`, page)

		resp, err := client.RPCClient.ABCIQuery("vm/qeval", []byte(cmd))
		if err != nil {
			return nil, err
		} else if resp.Response.Error != nil && resp.Response.Error.Error() != "" {
			return nil, errors.New(resp.Response.Error.Error())
		}

		data := string(resp.Response.Data)

		// Extract with regex
		re := regexp.MustCompile(`\[\s*([^\]]+?)\s*]\(/r/gnops/valopers:([a-z0-9]+)\)`)
		matches := re.FindAllStringSubmatch(data, -1)

		// If no result, we stop the loop
		if len(matches) == 0 {
			break
		}

		// Adding the valopers to this page
		for _, m := range matches {
			allValopers = append(allValopers, Valoper{
				Name:    m[1],
				Address: m[2],
			})
			// log.Printf("name %s addr %s", m[1], m[2])
		}

		log.Printf("✅ Fetched %d valopers from valopers.Render page %d\n", len(matches), page)

		page++
	}

	log.Printf("🎉 Total valopers fetched: %d\n", len(allValopers))
	return allValopers, nil
}
func GetGenesisMonikers(rpcURL string) (map[string]string, error) {
	url := fmt.Sprintf("%s/genesis", strings.TrimRight(rpcURL, "/"))

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch genesis: %w", err)
	}
	defer resp.Body.Close()

	type Validator struct {
		Address string `json:"address"`
		Name    string `json:"name"`
	}

	monikers := make(map[string]string)

	decoder := json.NewDecoder(resp.Body)

	// Go to the "validators" key
	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("error scanning JSON tokens: %w", err)
		}
		if key, ok := tok.(string); ok && key == "validators" {
			break
		}
	}

	// Check that the next token is indeed an array [
	tok, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("error reading validators array start: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return nil, errors.New("expected start of array for validators")
	}

	for decoder.More() {
		var current Validator
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("error decoding validator: %w", err)
		}
		monikers[current.Address] = current.Name
	}

	return monikers, nil
}

func InitMonikerMap(db *gorm.DB) {
	type Validator struct {
		Address string `json:"address"`
	}
	type ValidatorsResponse struct {
		Result struct {
			Validators []Validator `json:"validators"`
		} `json:"result"`
	}
	// Step 1 — Retrieve active validators from the RPC endpoint `/validators`
	url := fmt.Sprintf("%s/validators", strings.TrimRight(internal.Config.RPCEndpoint, "/"))
	var resp *http.Response
	err := doWithRetry(3, 2*time.Second, func() error {
		var e error
		resp, e = http.Get(url) // nolint:bodyclose // closed via defer resp.Body.Close() below
		return e
	})
	if err != nil {
		log.Printf("❌ Failed to retrieve validators after retries: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("❌ Error reading validator response: %v", err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("❌ Invalid HTTP status %d from /validators: %s", resp.StatusCode, string(body))
		return
	}

	if !json.Valid(body) {
		log.Printf("❌ Invalid JSON received from /validators:\n%s", string(body))
		return
	}
	var validatorsResp ValidatorsResponse
	if err := json.Unmarshal(body, &validatorsResp); err != nil {
		log.Printf("❌ Error decoding validator JSON: %v\nRaw body: %s", err, string(body))
		return
	}

	//Step 2 — Create Gno client for valopers.Render
	rpcClient, err := rpcclient.NewHTTPClient(internal.Config.RPCEndpoint)
	if err != nil {
		log.Printf("❌ Failed to create RPC client: %v", err)
		return
	}
	client := gnoclient.Client{RPCClient: rpcClient}

	valopers, err := GetValopers(client)
	if err != nil {
		log.Printf("⚠️ Failed to get valopers: %v", err)
	}

	valoperMap := make(map[string]string)
	for _, v := range valopers {
		valoperMap[v.Address] = v.Name
	}

	// Step #— Génésis monikers
	genesisMap, err := GetGenesisMonikers(internal.Config.RPCEndpoint)
	if err != nil {
		log.Printf("⚠️ Failed to get genesis monikers: %v", err)
	}
	// Step 3 — Monikers from DB
	dbMap, err := database.GetMoniker(db)
	if err != nil {
		log.Printf("⚠️ Failed to get monikers from DB: %v", err)
	}

	// Step 4 — Building a Complete and Prioritized MonikerMap
	MonikerMutex.Lock()
	defer MonikerMutex.Unlock()
	MonikerMap = make(map[string]string)

	for _, val := range validatorsResp.Result.Validators {
		addr := val.Address
		moniker := "unknown"
		if m, ok := dbMap[addr]; ok {
			moniker = m
		} else if m, ok := valoperMap[addr]; ok {
			moniker = m
		} else if m, ok := genesisMap[addr]; ok {
			moniker = m
		}

		MonikerMap[addr] = moniker
	}
	for addr, moniker := range MonikerMap {
		log.Printf("🔹 Validator: %s — Moniker: %s", addr, moniker)
	}

	log.Printf("✅ MonikerMap initialized with %d active validators\n", len(MonikerMap))

	// Sync MonikerMap to addr_monikers table
	for addr, moniker := range MonikerMap {
		if err := database.UpsertAddrMoniker(db, addr, moniker); err != nil {
			log.Printf("⚠️ Failed to upsert addr_moniker %s: %v", addr, err)
		}
	}
	log.Printf("✅ addr_monikers synced (%d entries)", len(MonikerMap))
}
func doWithRetry(attempts int, sleep time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		log.Printf("🔁 Retry %d/%d after error: %v", i+1, attempts, err)
		time.Sleep(sleep)
		sleep *= 2 // backoff
	}
	return fmt.Errorf("all retries failed: %w", err)
}
