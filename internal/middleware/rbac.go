package middleware

import (
	"net/http"

	"vetapp-backend/internal/models"
)

// RequireRole creates middleware that restricts access to users with specific roles.
// Pass one or more role constants (models.RoleOwner, models.RoleVet, models.RoleAdmin).
func RequireRole(roles ...int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			for _, role := range roles {
				if claims.GroupID == role {
					next.ServeHTTP(w, r)
					return
				}
			}

			http.Error(w, `{"error":"forbidden: insufficient permissions"}`, http.StatusForbidden)
		})
	}
}

// RequireVet is a shortcut for RequireRole(RoleVet, RoleAdmin).
// Admins can also access vet endpoints.
func RequireVet() func(http.Handler) http.Handler {
	return RequireRole(models.RoleVet, models.RoleAdmin)
}

// RequireOwner is a shortcut for RequireRole(RoleOwner).
func RequireOwner() func(http.Handler) http.Handler {
	return RequireRole(models.RoleOwner)
}

// RequireAdmin is a shortcut for RequireRole(RoleAdmin).
func RequireAdmin() func(http.Handler) http.Handler {
	return RequireRole(models.RoleAdmin)
}
