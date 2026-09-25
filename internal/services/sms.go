package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"vetapp-backend/internal/config"
)

// SMSService handles sending SMS via smsoffice.ge API.
type SMSService struct {
	cfg    *config.Config
	client *http.Client
}

// NewSMSService creates a new SMSService.
func NewSMSService(cfg *config.Config) *SMSService {
	return &SMSService{
		cfg: cfg,
		// 5s upper bound — if SMS Office is hung the request goroutine
		// (and any DB connection it holds) shouldn't block forever.
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Send sends an SMS message to the given phone number.
func (s *SMSService) Send(phone, message string) error {
	params := url.Values{
		"key":         {s.cfg.SMSApiKey},
		"destination": {phone},
		"sender":      {s.cfg.SMSSender},
		"content":     {message},
	}

	resp, err := s.client.Get(s.cfg.SMSURL + "?" + params.Encode())
	if err != nil {
		return fmt.Errorf("sms request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sms api error %d: %s", resp.StatusCode, string(body))
	}
	// SMSOffice reports a rejected message (bad number, no balance) with
	// HTTP 200 and {"Success":false,...}; without this check those were
	// counted as sent.
	var out struct {
		Success *bool
		Message string
	}
	if json.Unmarshal(body, &out) == nil && out.Success != nil && !*out.Success {
		return fmt.Errorf("sms api rejected: %s", out.Message)
	}

	return nil
}
