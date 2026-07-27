package services

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vetapp-backend/internal/config"
)

// BOGService integrates Bank of Georgia's current Payments API
// (api.bog.ge), the successor to the iPay gateway in ipay.go.
//
// iPay and this are the SAME bank, different generations: BOG's own
// docs list iPay as deprecated ("Ipay integration is no longer
// available. To integrate Bank of Georgia payment methods, please use
// the Payment Manager technical documentation."). Existing iPay
// merchants keep working, but the old gateway gets no new features —
// notably no Apple Pay / Google Pay and no modern saved-card flows.
//
// Both services are kept side by side so the switch is a config change
// (PAYMENT_PROVIDER) rather than a redeploy-and-pray migration, and so
// in-flight iPay orders can still be settled while new orders go to
// BOG.
//
// Docs: https://api.bog.ge/docs/en/payments/introduction
type BOGService struct {
	cfg    *config.Config
	client *http.Client

	// bogPublicKey caches the parsed callback-verification key so we
	// don't re-parse PEM on every webhook.
	bogPublicKey *rsa.PublicKey
}

// NewBOGService creates a BOGService.
func NewBOGService(cfg *config.Config) *BOGService {
	return &BOGService{
		cfg: cfg,
		// Bound every call so a hung gateway can't pin request
		// goroutines or DB connections (same reasoning as IPayService).
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled reports whether BOG credentials are configured. Callers use
// this to fall back to iPay rather than returning 500s on a deployment
// that hasn't been given BOG credentials yet.
func (s *BOGService) Enabled() bool {
	return s.cfg.BOGClientID != "" && s.cfg.BOGSecretKey != ""
}

// --- Auth ---

type bogTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// GetToken obtains an OAuth2 access token.
//
// Unlike iPay (which authenticates against the gateway host), BOG uses
// a separate Keycloak realm at oauth2.bog.ge.
func (s *BOGService) GetToken() (string, error) {
	data := url.Values{"grant_type": {"client_credentials"}}

	req, err := http.NewRequest(http.MethodPost,
		s.cfg.BOGAuthURL+"/auth/realms/bog/protocol/openid-connect/token",
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return "", fmt.Errorf("bog: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.cfg.BOGClientID, s.cfg.BOGSecretKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("bog: token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bog: token request status %d: %s", resp.StatusCode, truncateForLog(body))
	}

	var tr bogTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("bog: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("bog: empty access_token in response")
	}
	return tr.AccessToken, nil
}

// --- Orders ---

// BOGOrderResponse is the subset of the create-order response we use.
type BOGOrderResponse struct {
	OrderID     string
	RedirectURL string
}

type bogCreateOrderResponse struct {
	ID    string `json:"id"`
	Links struct {
		Redirect struct {
			Href string `json:"href"`
		} `json:"redirect"`
		Details struct {
			Href string `json:"href"`
		} `json:"details"`
	} `json:"_links"`
}

// CreateOrder opens a checkout order and returns the URL to send the
// customer to.
//
// `externalOrderID` is our own reference (used to correlate the
// callback) and doubles as the idempotency key, so a retried checkout
// for the same attempt cannot create two orders — important because a
// duplicate order means a customer can be charged twice.
//
// `amount` is a decimal string ("14.99") to avoid float rounding on
// money; BOG wants a JSON number, so it is emitted via json.Number.
func (s *BOGService) CreateOrder(token, amount, externalOrderID, callbackURL, successURL, failURL, locale string) (*BOGOrderResponse, error) {
	if strings.TrimSpace(amount) == "" {
		return nil, fmt.Errorf("bog: empty amount")
	}

	payload := map[string]any{
		"callback_url":      callbackURL,
		"external_order_id": externalOrderID,
		"purchase_units": map[string]any{
			"currency":     "GEL",
			"total_amount": json.Number(amount),
			"basket": []map[string]any{{
				"product_id": externalOrderID,
				"quantity":   1,
				"unit_price": json.Number(amount),
			}},
		},
		"redirect_urls": map[string]any{
			"success": successURL,
			"fail":    failURL,
		},
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("bog: marshal order: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost,
		s.cfg.BOGAPIURL+"/payments/v1/ecommerce/orders", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("bog: build order request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", externalOrderID)
	if locale != "" {
		// BOG renders its hosted page in this language.
		req.Header.Set("Accept-Language", locale)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bog: order request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("bog: order status %d: %s", resp.StatusCode, truncateForLog(body))
	}

	var or bogCreateOrderResponse
	if err := json.Unmarshal(body, &or); err != nil {
		return nil, fmt.Errorf("bog: decode order response: %w", err)
	}
	if or.ID == "" || or.Links.Redirect.Href == "" {
		return nil, fmt.Errorf("bog: order response missing id/redirect: %s", truncateForLog(body))
	}

	return &BOGOrderResponse{OrderID: or.ID, RedirectURL: or.Links.Redirect.Href}, nil
}

// BOGOrderStatus is the authoritative status of an order.
type BOGOrderStatus struct {
	OrderID       string
	Status        string // "completed", "rejected", "created", ...
	ExternalOrder string
	Amount        string
}

type bogOrderDetailsResponse struct {
	OrderID         string `json:"order_id"`
	ExternalOrderID string `json:"external_order_id"`
	OrderStatus     struct {
		Key string `json:"key"`
	} `json:"order_status"`
	PurchaseUnits struct {
		Transfer struct {
			Amount string `json:"amount"`
		} `json:"transfer_amount"`
	} `json:"purchase_units"`
}

// GetOrderStatus fetches the authoritative status straight from BOG.
//
// This is the linchpin of the security model: a webhook body is only
// ever a hint that something happened. Anyone can POST a callback
// claiming success, so the real status is always re-fetched with our
// own token before any subscription is activated. IPayService does the
// same — keep it that way.
func (s *BOGService) GetOrderStatus(orderID string) (*BOGOrderStatus, error) {
	token, err := s.GetToken()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet,
		s.cfg.BOGAPIURL+"/payments/v1/receipt/"+url.PathEscape(orderID), nil)
	if err != nil {
		return nil, fmt.Errorf("bog: build status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bog: status request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bog: status %d: %s", resp.StatusCode, truncateForLog(body))
	}

	var dr bogOrderDetailsResponse
	if err := json.Unmarshal(body, &dr); err != nil {
		return nil, fmt.Errorf("bog: decode status response: %w", err)
	}

	return &BOGOrderStatus{
		OrderID:       dr.OrderID,
		Status:        strings.ToLower(dr.OrderStatus.Key),
		ExternalOrder: dr.ExternalOrderID,
		Amount:        dr.PurchaseUnits.Transfer.Amount,
	}, nil
}

// IsPaid reports whether a status string means "money received".
func (s *BOGService) IsPaid(status string) bool {
	switch strings.ToLower(status) {
	case "completed", "success", "succeeded", "paid":
		return true
	}
	return false
}

// --- Callback signature verification ---

// VerifyCallbackSignature checks the `Callback-Signature` header
// (SHA256withRSA, base64) against BOG's published public key.
//
// `rawBody` MUST be the exact bytes received. BOG's docs are explicit
// that verification has to happen before deserialization because JSON
// field order is part of the signed payload — re-marshalling a decoded
// struct would reorder fields and invalidate an otherwise-good
// signature.
//
// Returns an error when a signature is present but wrong. When no key
// is configured it reports (false, nil): "not verified", not "invalid".
// Callers must treat unverified callbacks as untrusted hints and rely
// on GetOrderStatus — which is what the handler does — so a missing key
// degrades safely instead of blocking payments. BOG marks the header
// itself as optional, so absence alone must never activate anything.
func (s *BOGService) VerifyCallbackSignature(rawBody []byte, signatureB64 string) (bool, error) {
	if signatureB64 == "" {
		return false, nil
	}
	key, err := s.publicKey()
	if err != nil {
		return false, err
	}
	if key == nil {
		return false, nil
	}

	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return false, fmt.Errorf("bog: callback signature is not valid base64: %w", err)
	}

	digest := sha256.Sum256(rawBody)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return false, fmt.Errorf("bog: callback signature verification failed: %w", err)
	}
	return true, nil
}

// publicKey lazily parses the configured PEM public key.
func (s *BOGService) publicKey() (*rsa.PublicKey, error) {
	if s.bogPublicKey != nil {
		return s.bogPublicKey, nil
	}
	pemStr := strings.TrimSpace(s.cfg.BOGPublicKey)
	if pemStr == "" {
		return nil, nil
	}
	// Allow the key to be supplied as a single-line env var with
	// literal "\n" escapes, which is how most PaaS dashboards store
	// multi-line secrets.
	pemStr = strings.ReplaceAll(pemStr, `\n`, "\n")

	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("bog: BOG_PUBLIC_KEY is not valid PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		// Some deployments paste an RSA PUBLIC KEY (PKCS#1) instead.
		if rsaPub, err2 := x509.ParsePKCS1PublicKey(block.Bytes); err2 == nil {
			s.bogPublicKey = rsaPub
			return rsaPub, nil
		}
		return nil, fmt.Errorf("bog: parse public key: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("bog: BOG_PUBLIC_KEY is not an RSA key")
	}
	s.bogPublicKey = rsaPub
	return rsaPub, nil
}

// truncateForLog bounds gateway error bodies so a misbehaving upstream
// can't flood the logs.
func truncateForLog(b []byte) string {
	const max = 512
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
