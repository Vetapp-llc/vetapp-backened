package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"gorm.io/gorm"
)

const (
	// A PIN is read out to a vet in person and used at once; a short life
	// keeps the number of live PINs, and so the odds of guessing one, low.
	sharePINLifetime  = time.Hour
	shareLinkLifetime = 7 * 24 * time.Hour
)

// ShareTokenResponse is a share PIN or link token with its expiry.
type ShareTokenResponse struct {
	Token     string    `json:"token" validate:"required"`
	ExpiresAt time.Time `json:"expires_at" validate:"required"`
}

// issueSharePIN gives the pet a fresh 6-digit PIN, replacing its previous
// one. The PIN is the table's primary key, so two pets can never hold the
// same live PIN; expired rows are cleared first so their numbers free up.
// A per-pet lock keeps two simultaneous requests from leaving two PINs.
func issueSharePIN(db *gorm.DB, petID uint) (ShareTokenResponse, error) {
	expires := time.Now().Add(sharePINLifetime).UTC().Truncate(time.Second)
	var out ShareTokenResponse
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('pet-pin|' || ?))`, petID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM pet_share_tokens WHERE expires_at < NOW() OR (pet_id = ? AND kind = 'pin')`, petID).Error; err != nil {
			return err
		}
		for i := 0; i < 20; i++ {
			pin, err := numericCode(6)
			if err != nil {
				return err
			}
			res := tx.Exec(`INSERT INTO pet_share_tokens (token, pet_id, kind, expires_at)
				VALUES (?, ?, 'pin', ?) ON CONFLICT (token) DO NOTHING`, pin, petID, expires)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 1 {
				out = ShareTokenResponse{Token: pin, ExpiresAt: expires}
				return nil
			}
		}
		return errShareExhausted
	})
	return out, err
}

var errShareExhausted = errors.New("could not allocate a share code")

// newLinkToken stores a fresh random link token for the pet.
func newLinkToken(db *gorm.DB, petID uint, expires time.Time) (ShareTokenResponse, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return ShareTokenResponse{}, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	if err := db.Exec(`INSERT INTO pet_share_tokens (token, pet_id, kind, expires_at) VALUES (?, ?, 'link', ?)`,
		tok, petID, expires).Error; err != nil {
		return ShareTokenResponse{}, err
	}
	return ShareTokenResponse{Token: tok, ExpiresAt: expires}, nil
}

// shareLink returns the pet's link token, reusing the current one while
// it has more than a day left so a QR code on screen stays stable.
func shareLink(db *gorm.DB, petID uint) (ShareTokenResponse, error) {
	var cur ShareTokenResponse
	err := db.Raw(`SELECT token, expires_at FROM pet_share_tokens
		WHERE pet_id = ? AND kind = 'link' AND expires_at > NOW() + INTERVAL '1 day'
		ORDER BY expires_at DESC LIMIT 1`, petID).Scan(&cur).Error
	if err == nil && cur.Token != "" {
		return cur, nil
	}
	return newLinkToken(db, petID, time.Now().Add(shareLinkLifetime).UTC().Truncate(time.Second))
}

// shareGranted reports whether the request's `s` token opens petID.
// Only link tokens count: a PIN is exchanged for one by the rate-limited
// PIN lookup, so PINs cannot be guessed through these endpoints.
//
// PUBLIC_QR_ALLOW_UNSIGNED=1 re-opens the old bare-id links for a
// transition period (QR codes shown by app builds that predate tokens).
func shareGranted(db *gorm.DB, r *http.Request, petID uint) bool {
	if os.Getenv("PUBLIC_QR_ALLOW_UNSIGNED") == "1" {
		return true
	}
	tok := r.URL.Query().Get("s")
	if tok == "" || len(tok) > 64 {
		return false
	}
	var n int64
	db.Raw(`SELECT COUNT(*) FROM pet_share_tokens WHERE token = ? AND pet_id = ? AND kind = 'link' AND expires_at > NOW()`,
		tok, petID).Scan(&n)
	return n > 0
}

// petIDParam parses a numeric pet id, 0 if invalid.
func petIDParam(s string) uint {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return uint(n)
}
