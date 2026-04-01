package api

import (
	"net/http"

	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"gorm.io/gorm"
)

// adminAuthMiddleware protects all /admin routes with a static token check.
// In dev_mode the check is bypassed (same pattern as Clerk bypass).
func adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if internal.Config.DevMode {
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get("X-Admin-Token")
		if token == "" || token != internal.Config.AdminToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// registerAdminRoutes attaches all /admin/* handlers to mux.
// All routes are wrapped with adminAuthMiddleware.
// Route handlers are implemented in Phase 2.
func registerAdminRoutes(mux *http.ServeMux, db *gorm.DB) {
	adminHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		EnableCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "admin routes not yet implemented", http.StatusNotImplemented)
	})

	mux.Handle("/admin/", adminAuthMiddleware(adminHandler))
}
