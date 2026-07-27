package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"vetapp-backend/internal/config"
)

// IPayService handles iPay.ge payment gateway integration.
type IPayService struct {
	cfg    *config.Config
	client *http.Client
}

// NewIPayService creates a new IPayService.
func NewIPayService(cfg *config.Config) *IPayService {
	return &IPayService{
		cfg: cfg,
		// Bound every iPay HTTP call so a hung gateway can't pin our
		// request goroutines or DB connections.
		client: &http.Client{Timeout: 10 * time.Second},
	}
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

	resp, err := s.client.Do(req)
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
	shopOrderID := fmt.Sprintf("pet_%d_%d", petID, time.Now().Unix())
	payload := map[string]interface{}{
		"intent":         "CAPTURE",
		"shop_order_id":  shopOrderID,
		"redirect_url":   callbackURL,
		"capture_method": "AUTOMATIC",
		"locale":         "ka",
		"items": []map[string]interface{}{
			{
				"amount":      amount,
				"description": "VetApp Subscription",
				"quantity":    "1",
				"product_id":  shopOrderID,
			},
		},
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

	resp, err := s.client.Do(req)
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

// OrderStatus is the authoritative status for an iPay order, fetched
// from the gateway directly. This is what the Callback handler trusts —
// the request body that iPay sends to our webhook is treated as a
// notification, not as authoritative.
type OrderStatus struct {
	OrderID   string `json:"order_id"`
	Status    string `json:"status"`
	TransID   string `json:"transaction_id"`
	IndAmount string `json:"ind_amount"`
}

// GetOrderStatus fetches the order details directly from iPay using a
// fresh OAuth token. This is what the Callback handler uses to decide
// whether a payment really succeeded — never trust the webhook body.
func (s *IPayService) GetOrderStatus(orderID string) (*OrderStatus, error) {
	token, err := s.GetToken()
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	req, err := http.NewRequest("GET",
		s.cfg.IPayURL+"/opay/api/v1/checkout/orders/"+url.PathEscape(orderID),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ipay status request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ipay status error %d: %s", resp.StatusCode, string(body))
	}

	var raw struct {
		OrderID       string `json:"order_id"`
		OrderStatus   string `json:"order_status"`   // newer field name
		Status        string `json:"status"`         // older alias
		PaymentMethod struct {
			Type    string `json:"type"`
			TransID string `json:"transaction_id"`
		} `json:"payment_method"`
		TransactionID string `json:"transaction_id"` // top-level fallback
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}

	status := raw.OrderStatus
	if status == "" {
		status = raw.Status
	}
	tid := raw.PaymentMethod.TransID
	if tid == "" {
		tid = raw.TransactionID
	}

	return &OrderStatus{
		OrderID: raw.OrderID,
		Status:  status,
		TransID: tid,
	}, nil
}
