package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

var (
	monikerMap = make(map[string]string)
)

func main() {
	initMonikerMap()
	fmt.Println("\n--- Moniker Map ---")
	for addr, moniker := range monikerMap {
		fmt.Printf("Address: %s => Moniker: %s\n", addr, moniker)
	}

	verifyValidatorCount()
}
func initMonikerMap() {
	resp, err := http.Get("https://test6.api.onbloc.xyz/v1/blocks?limit=40")
	if err != nil {
		log.Fatalf("Erreur lors de la récupération des blocs : %v", err)
	}
	defer resp.Body.Close()

	// Lire tout le corps une seule fois
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Erreur lors de la lecture de la réponse : %v", err)
	}

	// Structure pour parser le JSON
	type Block struct {
		BlockProposer      string `json:"blockProposer"`
		BlockProposerLabel string `json:"blockProposerLabel"`
	}

	type Data struct {
		Items []Block `json:"items"`
	}

	type BlocksResponse struct {
		Data Data `json:"data"`
	}

	var blocksResp BlocksResponse
	if err := json.Unmarshal(body, &blocksResp); err != nil {
		log.Fatalf("Erreur de décodage JSON : %v", err)
	}

	// Afficher chaque pair blockProposer + blockProposerLabel
	for _, block := range blocksResp.Data.Items {
		//fmt.Printf("Address: %s | Moniker: %s\n", block.BlockProposer, block.BlockProposerLabel)
		monikerMap[block.BlockProposer] = block.BlockProposerLabel

	}
}

func verifyValidatorCount() {
	resp, err := http.Get("https://test6.api.onbloc.xyz/v1/stats/summary/accounts")
	if err != nil {
		log.Printf("Erreur récupération du résumé des comptes : %v", err)
		return
	}
	defer resp.Body.Close()

	var summaryResp struct {
		Data struct {
			Data struct {
				Validators int `json:"validators"`
			} `json:"data"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&summaryResp); err != nil {
		log.Printf("Erreur de décodage JSON résumé : %v", err)
		return
	}

	expected := summaryResp.Data.Data.Validators
	actual := len(monikerMap)
	log.Printf("Nombre de validateurs dans les blocs récupérés : %d / %d attendus\n", actual, expected)
}
