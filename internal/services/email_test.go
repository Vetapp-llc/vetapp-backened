package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vetapp-backend/internal/config"
)

// EmailService is a thin HTTP wrapper around Resend; the easiest way
// to exercise it is to point `resendAPIURL` at a local httptest server
// and assert on the request shape + response handling. We avoid using
// the real Resend API in tests for obvious reasons (network, cost,
// flakiness).

func TestEmailService_Disabled(t *testing.T) {
	// No API key → service must report Enabled() == false and refuse
	// to call Send. This is the contract SendEmailVerification relies
	// on to fall back to the "pending" stub response.
	svc := NewEmailService(&config.Config{ResendAPIKey: ""})
	if svc.Enabled() {
		t.Fatalf("Enabled() = true on empty config; want false")
	}
	if _, err := svc.SendHTML("a@b.com", "subj", "<p>x</p>", "x"); err == nil {
		t.Fatalf("SendHTML did not error on disabled service")
	}
}

func TestEmailService_SendsExpectedPayload(t *testing.T) {
	var captured sendEmailRequest
	var capturedAuth, capturedCT string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedCT = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"test-id-123"}`))
	}))
	defer srv.Close()

	// Repoint the package-level URL at our test server. Restore on
	// exit so other tests aren't affected.
	prev := resendAPIURL
	resendAPIURL = srv.URL
	defer func() { resendAPIURL = prev }()

	svc := NewEmailService(&config.Config{
		ResendAPIKey: "re_test_key",
		ResendFrom:   "VetApp <noreply@vetapp.ge>",
	})

	id, err := svc.SendHTML("user@example.com", "Verify your email", "<p>hi</p>", "hi")
	if err != nil {
		t.Fatalf("SendHTML returned error: %v", err)
	}
	if id != "test-id-123" {
		t.Errorf("returned id = %q, want %q", id, "test-id-123")
	}

	if capturedAuth != "Bearer re_test_key" {
		t.Errorf("Authorization header = %q, want %q", capturedAuth, "Bearer re_test_key")
	}
	if capturedCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", capturedCT)
	}
	if captured.From != "VetApp <noreply@vetapp.ge>" {
		t.Errorf("from = %q, want VetApp <noreply@vetapp.ge>", captured.From)
	}
	if captured.To != "user@example.com" {
		t.Errorf("to = %q, want user@example.com", captured.To)
	}
	if captured.Subject != "Verify your email" {
		t.Errorf("subject = %q", captured.Subject)
	}
	if captured.HTML != "<p>hi</p>" {
		t.Errorf("html = %q", captured.HTML)
	}
	if captured.Text != "hi" {
		t.Errorf("text = %q", captured.Text)
	}
}

func TestEmailService_PropagatesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"name":"unauthorized","message":"bad key"}`))
	}))
	defer srv.Close()

	prev := resendAPIURL
	resendAPIURL = srv.URL
	defer func() { resendAPIURL = prev }()

	svc := NewEmailService(&config.Config{ResendAPIKey: "re_bad", ResendFrom: "x@y.z"})
	_, err := svc.SendHTML("a@b.com", "s", "<p/>", "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	// The error message must surface the upstream status so logs are
	// debuggable; we deliberately don't pin the exact format because
	// it carries Resend's body verbatim.
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q does not contain status 401", err)
	}
}

func TestEmailService_SuccessButUnexpectedBody(t *testing.T) {
	// 2xx with a payload that doesn't match Resend's shape: the
	// service should not error, just return an empty id.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`OK`))
	}))
	defer srv.Close()

	prev := resendAPIURL
	resendAPIURL = srv.URL
	defer func() { resendAPIURL = prev }()

	svc := NewEmailService(&config.Config{ResendAPIKey: "re_ok", ResendFrom: "x@y.z"})
	id, err := svc.SendHTML("a@b.com", "s", "<p/>", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty (upstream returned non-JSON)", id)
	}
}
