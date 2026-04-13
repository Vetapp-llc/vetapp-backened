package services

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
)

// AppleRootCAPEM is Apple's Root CA - G3 certificate in PEM format.
// This is used to verify the x5c certificate chain in JWS signed transactions.
const AppleRootCAPEM = `-----BEGIN CERTIFICATE-----
MIICQzCCAcmgAwIBAgIILcX8iNLFS5UwCgYIKoZIzj0EAwMwZzEbMBkGA1UEAwwS
QXBwbGUgUm9vdCBDQSAtIEczMSYwJAYDVQQLDB1BcHBsZSBDZXJ0aWZpY2F0aW9u
IEF1dGhvcml0eTETMBEGA1UECgwKQXBwbGUgSW5jLjELMAkGA1UEBhMCVVMwHhcN
MTQwNDMwMTgxOTA2WhcNMzkwNDMwMTgxOTA2WjBnMRswGQYDVQQDDBJBcHBsZSBS
b290IENBIC0gRzMxJjAkBgNVBAsMHUFwcGxlIENlcnRpZmljYXRpb24gQXV0aG9y
aXR5MRMwEQYDVQQKDApBcHBsZSBJbmMuMQswCQYDVQQGEwJVUzB2MBAGByqGSM49
AgEGBSuBBAAiA2IABJjpLz1AcqTtkyJygRMc3RCV8cWjTnHcFBbZDuWmBSp3ZHtf
TjjTuxxEtX/1H7YyYl3J6YRbTzBPEVoA/VhYDKX1DyxNB0cTddqXl5dvMVztK515
1Du8SQ0/1P0RA16tlKNDMEEwHQYDVR0OBBYEFLuw3qFYM4iapIqZ3r6966/ayySr
MA8GA1UdEwEB/wQFMAMBAf8wDwYDVR0PAQH/BAUDAwEGMAoGCCqGSM49BAMDA2gA
MGUCMQCD6cHEFl4aXTQY2e3v9GwOAEZLuN+yRhHFD/3meoyhpmvOwgPUnPWTxnS4
at+qIxUCMG1mihDK1A3UT82NQz60imOlM27jbdoXt2QfyFMm+YhidDkLF1vLUagM
6BgD56KyKA==
-----END CERTIFICATE-----`

// AppleTransactionInfo contains the decoded fields from a StoreKit 2 signed transaction.
type AppleTransactionInfo struct {
	TransactionID       string `json:"transactionId"`
	OriginalTxID        string `json:"originalTransactionId"`
	BundleID            string `json:"bundleId"`
	ProductID           string `json:"productId"`
	PurchaseDate        int64  `json:"purchaseDate"`
	ExpiresDate         int64  `json:"expiresDate,omitempty"`
	Type                string `json:"type"`
	InAppOwnershipType  string `json:"inAppOwnershipType"`
	Environment         string `json:"environment"`
	SignedDate          int64  `json:"signedDate"`
}

// VerifyAppleJWS verifies a StoreKit 2 JWS signed transaction.
// It validates the x5c certificate chain against Apple's Root CA G3
// and verifies the ES256 signature.
func VerifyAppleJWS(signedTransaction string) (*AppleTransactionInfo, error) {
	parts := strings.Split(signedTransaction, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid JWS format: expected 3 parts")
	}

	headerBytes, err := base64URLDecode(parts[0])
	if err != nil {
		return nil, errors.New("failed to decode JWS header")
	}

	payloadBytes, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, errors.New("failed to decode JWS payload")
	}

	sigBytes, err := base64URLDecode(parts[2])
	if err != nil {
		return nil, errors.New("failed to decode JWS signature")
	}

	// Parse header to get x5c chain and algorithm
	var header struct {
		Alg string   `json:"alg"`
		X5C []string `json:"x5c"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, errors.New("failed to parse JWS header")
	}
	if header.Alg != "ES256" {
		return nil, errors.New("unsupported algorithm: " + header.Alg)
	}
	if len(header.X5C) < 2 {
		return nil, errors.New("x5c chain too short")
	}

	// Parse x5c certificates (DER encoded, base64)
	certs := make([]*x509.Certificate, len(header.X5C))
	for i, certB64 := range header.X5C {
		certDER, err := base64.StdEncoding.DecodeString(certB64)
		if err != nil {
			return nil, errors.New("failed to decode x5c certificate")
		}
		cert, err := x509.ParseCertificate(certDER)
		if err != nil {
			return nil, errors.New("failed to parse x5c certificate")
		}
		certs[i] = cert
	}

	// Parse Apple Root CA
	block, _ := pem.Decode([]byte(AppleRootCAPEM))
	if block == nil {
		return nil, errors.New("failed to decode Apple Root CA PEM")
	}
	appleRoot, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("failed to parse Apple Root CA")
	}

	// Build certificate pool and verify chain
	rootPool := x509.NewCertPool()
	rootPool.AddCert(appleRoot)

	intermediatePool := x509.NewCertPool()
	for _, cert := range certs[1:] {
		intermediatePool.AddCert(cert)
	}

	opts := x509.VerifyOptions{
		Roots:         rootPool,
		Intermediates: intermediatePool,
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return nil, errors.New("certificate chain verification failed: " + err.Error())
	}

	// Verify ES256 signature using leaf certificate's public key
	pubKey, ok := certs[0].PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("leaf certificate does not have ECDSA public key")
	}

	signedContent := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signedContent))

	// ES256 signature is r || s, each 32 bytes
	if len(sigBytes) != 64 {
		return nil, errors.New("invalid ES256 signature length")
	}
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])

	if !ecdsa.Verify(pubKey, hash[:], r, s) {
		return nil, errors.New("signature verification failed")
	}

	// Parse payload
	var txInfo AppleTransactionInfo
	if err := json.Unmarshal(payloadBytes, &txInfo); err != nil {
		return nil, errors.New("failed to parse transaction info")
	}

	return &txInfo, nil
}

// base64URLDecode handles base64url decoding with optional padding.
func base64URLDecode(s string) ([]byte, error) {
	// Add padding if missing
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}
