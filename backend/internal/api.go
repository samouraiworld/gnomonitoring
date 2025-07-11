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

	http.HandleFunc("/webhooksgovdao", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", Config.AllowOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		switch r.Method {
		case http.MethodGet:
			idParam := r.URL.Query().Get("id")
			if idParam != "" {
				id, err := strconv.Atoi(idParam)
				if err != nil || id <= 0 {
					http.Error(w, "Invalid ID", http.StatusBadRequest)
					return
				}
				webhook, err := GetWebhookByID(db, id, "webhooks_GovDAO")
				if err != nil {
					http.Error(w, fmt.Sprintf("DB error: %v", err), http.StatusInternalServerError)
					return
				}
				if webhook == nil {
					http.Error(w, "Webhook not found", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(webhook)
				return
			}

			// Sinon, liste complète
			webhooks, err := ListWebhooks(db)
			if err != nil {
				http.Error(w, fmt.Sprintf("Error List : %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(webhooks)

		case http.MethodPost:
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

		case http.MethodPut:
			var input struct {
				ID   int64  `json:"id"`
				USER string `json:"USER"`
				URL  string `json:"url"`
				Type string `json:"type"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "Invalid body", http.StatusBadRequest)
				return
			}
			if input.ID <= 0 || input.USER == "" || input.URL == "" || input.Type == "" {
				http.Error(w, "Missing or invalid fields", http.StatusBadRequest)
				return
			}

			err := UpdateMonitoringWebhook(db, input.ID, input.USER, input.URL, input.Type, "webhooks_GovDAO")
			if err != nil {
				http.Error(w, fmt.Sprintf("Update error: %v", err), http.StatusInternalServerError)
				return
			}
			w.Write([]byte("✅ Webhook updated."))

		case http.MethodDelete:
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
			w.Write([]byte("✅ Delete Webhook"))

		default:
			http.Error(w, "Unauthorized method", http.StatusMethodNotAllowed)
		}
	})

	// api for Monitoring Validator

	http.HandleFunc("/gnovalidator", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", Config.AllowOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

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

			idParam := r.URL.Query().Get("id")
			if idParam != "" {
				id, err := strconv.Atoi(idParam)
				if err != nil || id <= 0 {
					http.Error(w, "Invalid ID", http.StatusBadRequest)
					return
				}
				webhook, err := GetWebhookByID(db, id, "webhooks_validator")
				if err != nil {
					http.Error(w, fmt.Sprintf("DB error: %v", err), http.StatusInternalServerError)
					return
				}
				if webhook == nil {
					http.Error(w, "Webhook not found", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(webhook)
				return
			}

			hooks, err := ListMonitoringWebhooks(db)
			if err != nil {
				http.Error(w, fmt.Sprintf("List error: %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(hooks)

		case http.MethodPut:
			var input struct {
				ID   int64  `json:"id"`
				USER string `json:"USER"`
				URL  string `json:"url"`
				Type string `json:"type"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "Invalid body", http.StatusBadRequest)
				return
			}
			if input.ID <= 0 || input.USER == "" || input.URL == "" || input.Type == "" {
				http.Error(w, "Missing or invalid fields", http.StatusBadRequest)
				return
			}

			err := UpdateMonitoringWebhook(db, input.ID, input.USER, input.URL, input.Type, "webhooks_validator")
			if err != nil {
				http.Error(w, fmt.Sprintf("Update error: %v", err), http.StatusInternalServerError)
				return
			}
			w.Write([]byte("✅ Webhook updated."))

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
