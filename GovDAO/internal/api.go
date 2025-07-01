package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

type WebhookInput struct {
	URL  string `json:"url"`
	Type string `json:"type"` // "discord" ou "slack"
}

func StartWebhookAPI(db *sql.DB) {

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:3000")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

		if r.Method == http.MethodOptions {
			// Réponse vide pour valider la requête CORS préliminaire
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
			return
		}

		var input WebhookInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "Corps JSON invalide", http.StatusBadRequest)
			return
		}

		if input.URL == "" || (input.Type != "discord" && input.Type != "slack") {
			http.Error(w, "Paramètres invalides", http.StatusBadRequest)
			return
		}

		err := InsertWebhook(input.URL, input.Type, db)
		if err != nil {
			http.Error(w, fmt.Sprintf("Erreur insertion : %v", err), http.StatusInternalServerError)
			return
		}

		go StartWebhookWatcher(Webhook{
			URL:           input.URL,
			Type:          input.Type,
			LastCheckedID: 0,
		}, db)

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("✅ Webhook ajouté et lancé."))
	})

	http.HandleFunc("/webhooks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:3000")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			// Liste tous les webhooks
			webhooks, err := ListWebhooks(db)
			if err != nil {
				http.Error(w, fmt.Sprintf("Erreur liste : %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(webhooks)

		case http.MethodDelete:
			// Supprime un webhook avec ?id=xxx
			idStr := r.URL.Query().Get("id")
			id, err := strconv.Atoi(idStr)
			if err != nil || id <= 0 {
				http.Error(w, "Paramètre id invalide", http.StatusBadRequest)
				return
			}
			err = DeleteWebhook(id, db)
			if err != nil {
				http.Error(w, fmt.Sprintf("Erreur suppression : %v", err), http.StatusInternalServerError)
				return
			}
			w.Write([]byte("✅ Webhook supprimé"))
		default:
			http.Error(w, "Méthode non autorisée", http.StatusMethodNotAllowed)
		}
	})

	go func() {
		log.Println("🌐 API démarrée sur http://localhost:8080")
		log.Fatal(http.ListenAndServe(":8080", nil))
	}()
}
func StartWebhookWatcher(w Webhook, db *sql.DB) {
	ticker := time.NewTicker(time.Duration(Config.IntervallSecond) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			nextID := w.LastCheckedID + 1
			exists, title, moniker := ProposalExists(nextID)
			// log.Printf("check GovDao num %d\n", nextID)
			if exists {
				// msg := fmt.Sprintf("🗳️ *New Proposal:* %s\n_By %s_\n🔗source: <https://test6.testnets.gno.land/r/gov/dao:%d|View Proposal>", title, moniker, nextID)
				msg := fmt.Sprintf("--- \n 🗳️ ** New Proposal N° %d: %s ** - %s \n 🔗source: https://test6.testnets.gno.land/r/gov/dao:%d  ", nextID, title, moniker, nextID)
				msgSlack := fmt.Sprintf("--- \n 🗳️*New Proposal N° %d: %s* - %s_\n🔗source: https://test6.testnets.gno.land/r/gov/dao:%d  ", nextID, title, moniker, nextID)
				switch w.Type {
				case "discord":
					SendSingleDiscord(msg, w.URL)
				case "slack":
					SendSingleSlack(msgSlack, w.URL)
				}

				UpdateLastCheckedID(w.URL, nextID, db)
				w.LastCheckedID = nextID
			}
		}
	}
}
