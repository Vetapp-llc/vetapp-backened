package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"vetapp-backend/internal/config"
)

// IPayService handles iPay.ge payment gateway integration.
type IPayService struct {
	cfg *config.Config
}

// NewIPayService creates a new IPayService.
func NewIPayService(cfg *config.Config) *IPayService {
	return &IPayService{cfg: cfg}
}

// tokenResponse is the OAuth token response from iPay.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

// OrderResponse is the response from creating an iPay checkout order.
type OrderResponse struct {
	OrderID     string `json:"order_id"`
	RedirectURL string `json:"redirect_url"`
}

// GetToken obtains an OAuth2 access token from iPay.
func (s *IPayService) GetToken() (string, error) {
	data := url.Values{
		"grant_type": {"client_credentials"},
	}

	req, err := http.NewRequest("POST",
		s.cfg.IPayURL+"/opay/api/v1/oauth2/token",
		bytes.NewBufferString(data.Encode()),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.cfg.IPayClientID, s.cfg.IPaySecretKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ipay token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ipay token error %d: %s", resp.StatusCode, string(body))
	}

	var result tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	return result.AccessToken, nil
}

// CreateOrder creates a checkout order on iPay and returns the redirect URL.
func (s *IPayService) CreateOrder(token string, amount string, petID uint, callbackURL string) (*OrderResponse, error) {
	payload := map[string]interface{}{
		"intent":       "CAPTURE",
		"shop_order_id": fmt.Sprintf("pet_%d", petID),
		"redirect_url": callbackURL,
		"purchase_units": []map[string]interface{}{
			{
				"amount": map[string]string{
					"currency_code": "GEL",
					"value":         amount,
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal order: %w", err)
	}

	req, err := http.NewRequest("POST",
		s.cfg.IPayURL+"/opay/api/v1/checkout/orders",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ipay order request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ipay order error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		OrderID string `json:"order_id"`
		Links   []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode order response: %w", err)
	}

	redirectURL := ""
	for _, link := range result.Links {
		if link.Rel == "approve" {
			redirectURL = link.Href
			break
		}
	}

	return &OrderResponse{
		OrderID:     result.OrderID,
		RedirectURL: redirectURL,
	}, nil
}
