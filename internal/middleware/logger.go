package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"vetapp-backend/internal/services"
)

type loggerKey struct{}

// Logger logs each HTTP request with structured fields.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		reqID := chimw.GetReqID(r.Context())

		// Create a request-scoped logger with request_id
		log := slog.With("request_id", reqID)

		// Store logger in context for handlers
		ctx := context.WithValue(r.Context(), loggerKey{}, log)
		r = r.WithContext(ctx)

		// Wrap ResponseWriter to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		// Build log attributes
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", wrapped.statusCode),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("ip", r.RemoteAddr),
		}

		// Add user info if authenticated
		if claims, ok := r.Context().Value(UserClaimsKey).(*services.Claims); ok && claims != nil {
			attrs = append(attrs,
				slog.Int("user_id", int(claims.UserID)),
				slog.String("clinic", claims.Zip),
			)
		}

		attrArgs := make([]any, len(attrs))
		for i, a := range attrs {
			attrArgs[i] = a
		}

		if wrapped.statusCode >= 500 {
			log.Error("request", attrArgs...)
		} else if wrapped.statusCode >= 400 {
			log.Warn("request", attrArgs...)
		} else {
			log.Info("request", attrArgs...)
		}
	})
}

// RequestLogger returns the request-scoped logger with request_id and user context.
// Falls back to slog.Default() if no logger in context.
func RequestLogger(r *http.Request) *slog.Logger {
	if log, ok := r.Context().Value(loggerKey{}).(*slog.Logger); ok {
		// Enrich with user context if available
		if claims, ok := r.Context().Value(UserClaimsKey).(*services.Claims); ok && claims != nil {
			return log.With("user_id", claims.UserID, "clinic", claims.Zip)
		}
		return log
	}
	return slog.Default()
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
