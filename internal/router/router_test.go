// Routing-table tests. These exist because of a class of bug that is
// invisible in code review and silent at runtime: Chi resolves a
// request against the FIRST subrouter mounted at a matching prefix, so
// mounting r.Route("/api/auth", ...) and then registering /auth/me
// inside r.Route("/api", ...) makes /api/auth/me return 404 forever —
// no error, no log line, just a dead endpoint. The mobile app's
// profile screen, password change, and email verification all broke
// this way.
//
// We assert on the routing table itself rather than serving requests
// so the tests need no database or live services.
package router

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
)

// routes walks the built router and returns the set of "METHOD /path"
// pairs it can actually dispatch.
func routes(t *testing.T, r chi.Routes) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		found[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	return found
}

// TestAuthenticatedAuthRoutesAreReachable pins the endpoints the mobile
// app depends on for profile + credential management.
func TestAuthenticatedAuthRoutesAreReachable(t *testing.T) {
	r := Setup(nil, nil, nil, nil, nil, nil, nil, "ipay", "http://localhost:8080", "")
	found := routes(t, r)

	for _, want := range []string{
		"GET /api/auth/me",
		"PUT /api/auth/me",
		"POST /api/auth/change-password",
		"POST /api/auth/email/verify/send",
	} {
		if !found[want] {
			t.Errorf("route %q is not registered — mobile app would get 404", want)
		}
	}
}

// TestPublicAuthRoutesAreReachable guards the other half: the fix for
// the above must not shadow the unauthenticated entry points.
func TestPublicAuthRoutesAreReachable(t *testing.T) {
	r := Setup(nil, nil, nil, nil, nil, nil, nil, "ipay", "http://localhost:8080", "")
	found := routes(t, r)

	for _, want := range []string{
		"POST /api/auth/login",
		"POST /api/auth/register",
		"POST /api/auth/refresh",
		"POST /api/auth/otp/send",
		"POST /api/auth/otp/verify",
		"POST /api/auth/password-reset",
		"GET /api/auth/email/verify/confirm",
	} {
		if !found[want] {
			t.Errorf("public route %q is not registered", want)
		}
	}
}

// TestOwnerPortalRoutesAreReachable covers the rest of the mobile
// app's surface — every screen in the owner app maps to one of these.
func TestOwnerPortalRoutesAreReachable(t *testing.T) {
	r := Setup(nil, nil, nil, nil, nil, nil, nil, "ipay", "http://localhost:8080", "")
	found := routes(t, r)

	for _, want := range []string{
		"GET /api/owner/pets",
		"POST /api/owner/pets",
		"GET /api/owner/pets/{id}",
		"PUT /api/owner/pets/{id}",
		"GET /api/owner/pets/{id}/procedures",
		"POST /api/owner/pets/{id}/procedures",
		"DELETE /api/owner/pets/{id}/procedures/{procId}",
		"GET /api/owner/pets/{id}/diseases",
		"GET /api/owner/pets/{id}/code",
		"GET /api/owner/calendar",
		"GET /api/owner/visits",
		"GET /api/procedures/types",
		"GET /api/procedures/vaccine-options",
		"GET /api/procedures/test-options",
		"GET /api/procedures/dehel-options",
		"GET /api/procedures/ecto-options",
		"GET /api/owner/pets/{id}/procedures/{procId}/files",
		"POST /api/owner/pets/{id}/procedures/{procId}/files",
		"GET /api/owner/pets/{id}/procedures/{procId}/files/{fileId}",
		"DELETE /api/owner/pets/{id}/procedures/{procId}/files/{fileId}",
		"POST /api/subscriptions/checkout",
		"POST /api/subscriptions/apple-verify",
	} {
		if !found[want] {
			t.Errorf("owner route %q is not registered", want)
		}
	}
}
