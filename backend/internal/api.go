package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
)

type WebhookInput struct {
	USER string `json:"USER"`
	URL  string `json:"url"`
	Type string `json:"type"` // "discord" or ou "slack"
}

func StartWebhookAPI(db *sql.DB) {

	http.HandleFunc("/webhookgovdao", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", Config.AllowOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

		if r.Method == http.MethodOptions {
			// Réponse vempty for validate CORS
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Unauthorized method", http.StatusMethodNotAllowed)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Unauthorized method", http.StatusMethodNotAllowed)
			return
		}

		var input WebhookInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		if input.USER == "" || input.URL == "" || (input.Type != "discord" && input.Type != "slack") {
			http.Error(w, "Paramètres invalides", http.StatusBadRequest)
			return
		}

		err := InsertWebhook(input.USER, input.URL, input.Type, db)
		if err != nil {
			http.Error(w, fmt.Sprintf("Erreur insertion : %v", err), http.StatusInternalServerError)
			return
		}

		go StartWebhookWatcher(WebhookGovDao{
			URL:           input.URL,
			Type:          input.Type,
			LastCheckedID: 0,
		}, db)

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("✅ Add Webhook."))
	})

	http.HandleFunc("/webhooksgovdao", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", Config.AllowOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			// List of webhooks
			webhooks, err := ListWebhooks(db)
			if err != nil {
				http.Error(w, fmt.Sprintf("Error List : %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(webhooks)

		case http.MethodDelete:
			// Delete Webhook with  ?id=xxx
			idStr := r.URL.Query().Get("id")
			id, err := strconv.Atoi(idStr)
			if err != nil || id <= 0 {
				http.Error(w, "Invalid id Param", http.StatusBadRequest)
				return
			}
			err = DeleteWebhook(id, db)
			if err != nil {
				http.Error(w, fmt.Sprintf("Error Delete : %v", err), http.StatusInternalServerError)
				return
			}
			w.Write([]byte("✅ Delete Whebook"))
		default:
			http.Error(w, "Unauthorized method", http.StatusMethodNotAllowed)
		}
	})

	// api for Monitoring Validator

	http.HandleFunc("/gnovalidator", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", Config.AllowOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		switch r.Method {
		case http.MethodPost:
			var input WebhookInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "Invalid body", http.StatusBadRequest)
				return
			}
			if input.URL == "" || input.Type == "" || input.USER == "" {
				http.Error(w, "Missing fields", http.StatusBadRequest)
				return
			}
			err := InsertMonitoringWebhook(input.USER, input.URL, input.Type, db)
			if err != nil {
				http.Error(w, fmt.Sprintf("Insert Error: %v", err), http.StatusInternalServerError)
				return
			}
			w.Write([]byte("✅ Add Webhook ."))

		case http.MethodGet:
			hooks, err := ListMonitoringWebhooks(db)
			if err != nil {
				http.Error(w, fmt.Sprintf("List error: %v", err), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(hooks)

		case http.MethodDelete:
			idStr := r.URL.Query().Get("id")
			id, err := strconv.Atoi(idStr)
			if err != nil || id <= 0 {
				http.Error(w, "Invalid ID", http.StatusBadRequest)
				return
			}
			err = DeleteMonitoringWebhook(id, db)
			if err != nil {
				http.Error(w, fmt.Sprintf("Delete error: %v", err), http.StatusInternalServerError)
				return
			}
			w.Write([]byte("✅ Delete Webhook."))
		default:
			http.Error(w, "Unauthorized method", http.StatusMethodNotAllowed)
		}
	})

	go func() {

		addr := fmt.Sprintf(":%s", Config.BackendPort)
		log.Printf("🌐 Launch API on http://localhost%s", addr)
		log.Fatal(http.ListenAndServe(addr, nil))
	}()
}
