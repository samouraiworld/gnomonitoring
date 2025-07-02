package internal

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
)

type Valoper struct {
	Name    string
	Address string
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			NextProtos: []string{"http/1.1"},
		},
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 10 * time.Second,
		}).DialContext,
	},
	Timeout: 15 * time.Second,
}

func GetValopers(client gnoclient.Client) ([]Valoper, error) {
	resp, err := client.RPCClient.ABCIQuery("vm/qeval", []byte(`gno.land/r/gnoland/valopers.Render("")`))
	if err != nil {
		return nil, err
	} else if resp.Response.Error != nil && resp.Response.Error.Error() != "" {
		return nil, errors.New(resp.Response.Error.Error())
	}

	re := regexp.MustCompile(`\[\s*([^\]]+?)\s*]\(/r/gnoland/valopers:([a-z0-9]+)\)`)
	matches := re.FindAllStringSubmatch(string(resp.Response.Data), -1)

	valopers := make([]Valoper, 0, len(matches))
	for _, m := range matches {
		valopers = append(valopers, Valoper{
			Name:    m[1],
			Address: m[2],
		})
	}
	return valopers, nil
}
func GetGenesisMonikers() (map[string]string, error) {
	resp, err := http.Get("https://rpc.test6.testnets.gno.land/genesis")
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

	//Check that the next token is indeed an array [
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

func InitMonikerMap() {
	// Step 1 — Retrieve active validators from the RPC endpoint `/validators`
	url := fmt.Sprintf("%s/validators", strings.TrimRight(Config.RPCEndpoint, "/"))
	resp, err := http.Get(url)
	if err != nil {
		log.Fatalf("Error retrieving validators: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Error reading validator response: %v", err)
	}

	type Validator struct {
		Address string `json:"address"`
	}
	type ValidatorsResponse struct {
		Result struct {
			Validators []Validator `json:"validators"`
		} `json:"result"`
	}

	var validatorsResp ValidatorsResponse
	if err := json.Unmarshal(body, &validatorsResp); err != nil {
		log.Fatalf("Error decoding validator JSON: %v", err)
	}

	//Step 2 — Create Gno client for valopers.Render
	rpcClient, err := rpcclient.NewHTTPClient(Config.RPCEndpoint)
	if err != nil {
		log.Fatalf("Failed to create RPC client: %v", err)
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
	genesisMap, err := GetGenesisMonikers()
	if err != nil {
		log.Printf("⚠️ Failed to get genesis monikers: %v", err)
	}

	// Step 4 — Building a Complete and Prioritized MonikerMap
	MonikerMutex.Lock()
	defer MonikerMutex.Unlock()
	MonikerMap = make(map[string]string)

	for _, val := range validatorsResp.Result.Validators {
		addr := val.Address
		moniker := "inconnu"

		if m, ok := valoperMap[addr]; ok {
			moniker = m
		} else if m, ok := genesisMap[addr]; ok {
			moniker = m
		}

		MonikerMap[addr] = moniker
	}

	log.Printf("✅ MonikerMap initialized with %d active validators\n", len(MonikerMap))
}
