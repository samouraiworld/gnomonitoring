package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
)

type GenesisValidator struct {
	Address string `json:"address"`
	Name    string `json:"name"` // le moniker
}

type GenesisDoc struct {
	Validators []GenesisValidator `json:"validators"`
}

func main() {
	// Ouvre le fichier genesis.json
	data, err := os.ReadFile("genesis.json")
	if err != nil {
		log.Fatalf("Erreur lecture genesis.json: %v", err)
	}

	var genesis GenesisDoc
	if err := json.Unmarshal(data, &genesis); err != nil {
		log.Fatalf("Erreur parsing JSON: %v", err)
	}

	// Affiche tous les monikers
	for _, val := range genesis.Validators {
		fmt.Printf("Adresse: %s | Moniker: %s\n", val.Address, val.Name)
	}
}
