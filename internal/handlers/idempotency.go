package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"vetapp-backend/internal/middleware"

	"gorm.io/gorm"
)

var errIdempotencyMismatch = errors.New("idempotency key reused for a different request")

// idempotencyKey returns the request's Idempotency-Key header, bounded.
func idempotencyKey(r *http.Request) string {
	k := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(k) > 200 {
		return ""
	}
	return k
}

// claimIdempotency runs inside the write's transaction. With no key it
// returns (0, nil): proceed normally. Otherwise it claims the key; if
// the key was already used by this user for this route and the same
// request (fingerprint — a hash of the decoded body), it returns the id
// of the resource that request created, and the caller returns that
// resource instead of writing again. The same key with a different body
// is errIdempotencyMismatch: replaying the first result would report,
// say, a card payment that was actually recorded as cash.
func claimIdempotency(tx *gorm.DB, r *http.Request, route string, body interface{}) (existing uint, err error) {
	key := idempotencyKey(r)
	if key == "" {
		return 0, nil
	}
	fp := fingerprint(body)
	user := middleware.GetClaims(r).UserID
	res := tx.Exec(`INSERT INTO idempotency_keys (user_id, key, route, fingerprint) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, user, key, route, fp)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 1 {
		return 0, nil
	}
	var row struct {
		Route       string
		ResourceID  uint
		Fingerprint string
	}
	if err := tx.Raw(`SELECT route, resource_id, fingerprint FROM idempotency_keys WHERE user_id = ? AND key = ?`, user, key).Scan(&row).Error; err != nil {
		return 0, err
	}
	if row.Route != route || row.ResourceID == 0 || row.Fingerprint != fp {
		return 0, errIdempotencyMismatch
	}
	return row.ResourceID, nil
}

// recordIdempotency stores the created resource against the claimed key.
func recordIdempotency(tx *gorm.DB, r *http.Request, id uint) error {
	key := idempotencyKey(r)
	if key == "" {
		return nil
	}
	return tx.Exec(`UPDATE idempotency_keys SET resource_id = ? WHERE user_id = ? AND key = ?`,
		id, middleware.GetClaims(r).UserID, key).Error
}

// fingerprint hashes the decoded request so that two bodies meaning the
// same thing (key order, whitespace) compare equal.
func fingerprint(body interface{}) string {
	b, err := json.Marshal(body)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
