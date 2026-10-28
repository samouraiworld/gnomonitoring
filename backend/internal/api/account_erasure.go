package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

func writeAccountErasureError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, database.ErrAccountErased) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": "account_erased", "error": "This monitoring account was erased."})
	return true
}

// The authoritative check remains inside each DB write's locked transaction.
// This early check also returns the stable erasure response before validating
// references to rows already removed by erasure.
func rejectErasedAccount(w http.ResponseWriter, db *gorm.DB, userID string) bool {
	erased, err := database.IsAccountErased(db, userID)
	if err != nil {
		http.Error(w, "Account state unavailable", http.StatusInternalServerError)
		return true
	}
	if erased {
		return writeAccountErasureError(w, database.ErrAccountErased)
	}
	return false
}

// EraseAccountHandler is a separate irreversible operation. DELETE /users
// continues to reset alerts without erasing the sign-in identity's eligibility.
func EraseAccountHandler(w http.ResponseWriter, r *http.Request, db *gorm.DB) {
	EnableCORS(w, r)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, err := authUserIDFromContext(r)
	if err != nil || userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := database.EraseAccount(db.WithContext(r.Context()), userID); err != nil {
		http.Error(w, "Account erasure failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func registerAccountErasureRoute(mux *http.ServeMux, db *gorm.DB, protect middleware) {
	mux.Handle("/users/erase", corsThenAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { EraseAccountHandler(w, r, db) }), protect))
}
