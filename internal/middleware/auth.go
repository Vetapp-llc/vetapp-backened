package middleware

import (
	"context"
	"net/http"
	"strings"

	"vetapp-backend/internal/services"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const UserClaimsKey contextKey = "user_claims"

// Auth creates JWT authentication middleware.
// It extracts the token from the Authorization header, validates it,
// and stores the claims in the request context.
func Auth(authService *services.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get token from Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
				return
			}

			// Expect "Bearer <token>"
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || parts[0] != "Bearer" {
				http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
				return
			}

			// Validate token
			claims, err := authService.ValidateAccessToken(parts[1])
			if err != nil {
				http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}

			// Store claims in context for downstream handlers
			ctx := context.WithValue(r.Context(), UserClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetClaims extracts user claims from the request context.
// Returns nil if no claims found (should not happen after Auth middleware).
func GetClaims(r *http.Request) *services.Claims {
	claims, ok := r.Context().Value(UserClaimsKey).(*services.Claims)
	if !ok {
		return nil
	}
	return claims
}
