package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"testing"
)

func TestAppleRootCA(t *testing.T) {
	if _, err := getAppleRootPool(); err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode([]byte(AppleRootCAPEM))
	if block == nil {
		t.Fatal("Apple root certificate is missing")
	}
	fingerprint := sha256.Sum256(block.Bytes)
	const want = "63343abfb89a6a03ebb57e9b3f5fa7be7c4f5c756f3017b3a8c488c3653e9179"
	if got := hex.EncodeToString(fingerprint[:]); got != want {
		t.Fatalf("Apple Root CA - G3 SHA-256 fingerprint = %s, want %s", got, want)
	}
}
