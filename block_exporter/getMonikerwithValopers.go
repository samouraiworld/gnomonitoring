package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
)

func main() {
	addr := "g1tq3gyzjmuu4gzu4np4ckfgun87j540gvx43d65"
	addr := 
	url := fmt.Sprintf("https://test6.testnets.gno.land/r/gnoland/valopers/v2:%s", addr)

	resp, err := http.Get(url)
	if err != nil {
		log.Fatalf("Erreur HTTP: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Fatalf("Status code non OK: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Erreur lecture réponse: %v", err)
	}

	// Cherche : <h2 id="...">moniker</h2>
	re := regexp.MustCompile(`<h2 id="[^"]+">([^<]+)</h2>`)
	matches := re.FindStringSubmatch(string(body))
	if len(matches) >= 2 {
		moniker := matches[1]
		fmt.Println("Moniker trouvé :", moniker)
	} else {
		fmt.Println("Moniker non trouvé")
	}
}
