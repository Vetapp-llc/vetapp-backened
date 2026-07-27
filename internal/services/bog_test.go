package services

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"vetapp-backend/internal/config"
)

// newTestKey generates a throwaway RSA key and returns the service
// configured with its public half, plus the private half for signing.
func newTestKey(t *testing.T) (*BOGService, *rsa.PrivateKey) {
	t.Helper()
	// 2048 is the smallest size that is both realistic and fast enough
	// for a unit test.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	return NewBOGService(&config.Config{BOGPublicKey: string(pemBytes)}), priv
}

func sign(t *testing.T, priv *rsa.PrivateKey, body []byte) string {
	t.Helper()
	digest := sha256.Sum256(body)
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// A correctly signed callback must verify.
func TestVerifyCallbackSignature_Valid(t *testing.T) {
	svc, priv := newTestKey(t)
	body := []byte(`{"event":"order_payment","body":{"order_id":"abc"}}`)

	ok, err := svc.VerifyCallbackSignature(body, sign(t, priv, body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("valid signature did not verify")
	}
}

// A tampered body must NOT verify. This is the case that matters: an
// attacker flipping a status to "completed" would otherwise activate a
// subscription nobody paid for.
func TestVerifyCallbackSignature_TamperedBody(t *testing.T) {
	svc, priv := newTestKey(t)
	original := []byte(`{"event":"order_payment","body":{"order_id":"abc","status":"rejected"}}`)
	sig := sign(t, priv, original)

	tampered := []byte(`{"event":"order_payment","body":{"order_id":"abc","status":"completed"}}`)
	ok, err := svc.VerifyCallbackSignature(tampered, sig)
	if ok {
		t.Error("tampered body verified — forged callbacks would be accepted")
	}
	if err == nil {
		t.Error("tampered body should report an error, not a silent false")
	}
}

// A signature from the wrong key must not verify.
func TestVerifyCallbackSignature_WrongKey(t *testing.T) {
	svc, _ := newTestKey(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	body := []byte(`{"event":"order_payment"}`)

	ok, _ := svc.VerifyCallbackSignature(body, sign(t, other, body))
	if ok {
		t.Error("signature from an unrelated key verified")
	}
}

// No signature header → (false, nil): "not verified", not "invalid".
// The handler then relies on re-fetching the authoritative status.
// BOG marks the header optional, so this must not be a hard error.
func TestVerifyCallbackSignature_Absent(t *testing.T) {
	svc, _ := newTestKey(t)
	ok, err := svc.VerifyCallbackSignature([]byte(`{}`), "")
	if ok {
		t.Error("empty signature reported as verified")
	}
	if err != nil {
		t.Errorf("absent signature should not error, got %v", err)
	}
}

// No configured key → (false, nil), so a deployment without the key
// degrades to re-fetch rather than rejecting every callback.
func TestVerifyCallbackSignature_NoKeyConfigured(t *testing.T) {
	svc := NewBOGService(&config.Config{})
	ok, err := svc.VerifyCallbackSignature([]byte(`{}`), "AAAA")
	if ok {
		t.Error("verified against a nonexistent key")
	}
	if err != nil {
		t.Errorf("missing key should not error, got %v", err)
	}
}

// Garbage base64 is a malformed request, not a silent pass.
func TestVerifyCallbackSignature_MalformedBase64(t *testing.T) {
	svc, _ := newTestKey(t)
	ok, err := svc.VerifyCallbackSignature([]byte(`{}`), "!!!not-base64!!!")
	if ok {
		t.Error("malformed signature verified")
	}
	if err == nil {
		t.Error("malformed base64 should report an error")
	}
}

// PaaS dashboards store multi-line secrets as one line with literal
// \n escapes; the key must still parse.
func TestPublicKey_EscapedNewlines(t *testing.T) {
	svc, priv := newTestKey(t)
	escaped := ""
	for _, r := range svc.cfg.BOGPublicKey {
		if r == '\n' {
			escaped += `\n`
			continue
		}
		escaped += string(r)
	}
	svc2 := NewBOGService(&config.Config{BOGPublicKey: escaped})

	body := []byte(`{"event":"order_payment"}`)
	ok, err := svc2.VerifyCallbackSignature(body, sign(t, priv, body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("key with escaped newlines failed to verify a good signature")
	}
}

// Enabled() gates the fallback to the legacy gateway.
func TestBOGEnabled(t *testing.T) {
	if NewBOGService(&config.Config{}).Enabled() {
		t.Error("Enabled() true with no credentials")
	}
	if NewBOGService(&config.Config{BOGClientID: "id"}).Enabled() {
		t.Error("Enabled() true with only a client id")
	}
	if !NewBOGService(&config.Config{BOGClientID: "id", BOGSecretKey: "sec"}).Enabled() {
		t.Error("Enabled() false with full credentials")
	}
}

// IsPaid must accept the gateway's success vocabulary and nothing else
// — a false positive here activates an unpaid subscription.
func TestIsPaid(t *testing.T) {
	svc := NewBOGService(&config.Config{})
	for _, s := range []string{"completed", "COMPLETED", "success", "Succeeded", "paid"} {
		if !svc.IsPaid(s) {
			t.Errorf("IsPaid(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "created", "rejected", "failed", "pending", "refunded"} {
		if svc.IsPaid(s) {
			t.Errorf("IsPaid(%q) = true, want false", s)
		}
	}
}
