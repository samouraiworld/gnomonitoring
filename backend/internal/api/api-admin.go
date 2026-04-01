package api

import (
	"encoding/json"
	"log"
	"net/http"

	clerk "github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"gorm.io/gorm"
)

// adminRoleMiddleware checks that the authenticated Clerk user has { "role": "admin" }
// in their publicMetadata. Must be chained after clerkhttp.RequireHeaderAuthorization().
func adminRoleMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := clerk.SessionClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		u, err := clerkuser.Get(r.Context(), claims.Subject)
		if err != nil {
			log.Printf("[admin] failed to fetch user %s: %v", claims.Subject, err)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var meta map[string]interface{}
		if len(u.PublicMetadata) > 0 {
			if err := json.Unmarshal(u.PublicMetadata, &meta); err != nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}

		role, _ := meta["role"].(string)
		if role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// registerAdminRoutes attaches all /admin/* handlers to mux.
// In production: Clerk JWT validation + admin role check.
// In dev_mode: no auth required.
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

	if internal.Config.DevMode {
		mux.Handle("/admin/", adminHandler)
	} else {
		clerkProtected := clerkhttp.RequireHeaderAuthorization()
		mux.Handle("/admin/", clerkProtected(adminRoleMiddleware(adminHandler)))
	}
}
