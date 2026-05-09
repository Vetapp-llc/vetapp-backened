package services

// EmailService — thin wrapper around the Resend HTTP API
// (https://resend.com/docs/api-reference/emails/send-email).
//
// We deliberately avoid pulling in `github.com/resend/resend-go` here:
// the only call we make is a single POST, the API surface is small,
// and adding a SDK module just for that bloats the dependency graph.
//
// Configuration (see internal/config/config.go):
//
//   RESEND_API_KEY  — required for real sends; empty disables service
//   RESEND_FROM     — sender address, e.g. "VetApp <noreply@vetapp.ge>"
//
// Sandbox note: a fresh Resend account can only send from
// `onboarding@resend.dev` and only to the API-key owner's mailbox until
// a domain is verified via DNS in the Resend dashboard.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"vetapp-backend/internal/config"
)

// resendAPIURL is the Resend transactional-email endpoint. Defined as a
// var so tests can repoint it to a httptest server.
var resendAPIURL = "https://api.resend.com/emails"

// EmailService sends transactional emails through Resend.
type EmailService struct {
	cfg    *config.Config
	client *http.Client
}

// NewEmailService creates a new EmailService.
func NewEmailService(cfg *config.Config) *EmailService {
	return &EmailService{
		cfg: cfg,
		// 10s is plenty for a single Resend round-trip; we don't want
		// the request thread to block forever if Resend is slow.
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether the service is configured to actually send.
// Callers (e.g. SendEmailVerification) should consult this before
// generating a token so the user gets a meaningful error message
// instead of a cryptic 401 from Resend.
func (s *EmailService) Enabled() bool {
	return s != nil && s.cfg != nil && s.cfg.ResendAPIKey != ""
}

// SendEmailRequest is the JSON body Resend expects.
type sendEmailRequest struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html,omitempty"`
	Text    string `json:"text,omitempty"`
}

// SendHTML sends a transactional email with both an HTML body and a
// plaintext fallback. The plaintext is used by clients that strip HTML
// (and improves deliverability — Gmail/Outlook penalize HTML-only).
//
// Returns the Resend message ID on success so callers can correlate
// with their dashboard.
func (s *EmailService) SendHTML(to, subject, html, text string) (string, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("email service not configured (RESEND_API_KEY unset)")
	}

	body, err := json.Marshal(sendEmailRequest{
		From:    s.cfg.ResendFrom,
		To:      to,
		Subject: subject,
		HTML:    html,
		Text:    text,
	})
	if err != nil {
		return "", fmt.Errorf("marshal resend payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, resendAPIURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.ResendAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resend request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("resend api error %d: %s", resp.StatusCode, string(respBody))
	}

	// Resend success response: {"id": "..."}.
	var ok struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &ok); err != nil {
		// Successful 2xx but unexpected body — return success without an
		// id rather than treating this as an error.
		return "", nil
	}
	return ok.ID, nil
}
