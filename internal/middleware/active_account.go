package middleware

import (
	"net/http"
	"sync"
	"time"

	"gorm.io/gorm"
)

// ActiveAccount rejects a valid token whose account has since been
// disabled or moved: status no longer T, or a role or clinic different
// from the one in the token. Without it a removed vet kept full access
// until the token expired (24 h) — long enough to add a replacement
// account for themselves through /staff.
//
// The account row is cached for accountCacheTTL so this costs one
// primary-key lookup per user per TTL, not one per request. Handlers that
// disable or move an account call InvalidateAccount, so their change
// takes effect on the next request rather than after the TTL.
func ActiveAccount(db *gorm.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				next.ServeHTTP(w, r)
				return
			}
			acct, ok := lookupAccount(db, claims.UserID)
			if !ok || acct.Status != "T" || acct.GroupID != claims.GroupID || acct.Zip != claims.Zip {
				http.Error(w, `{"error":"account disabled or changed; sign in again"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

const accountCacheTTL = 30 * time.Second

type accountState struct {
	Status  string
	GroupID int
	Zip     string
	loaded  time.Time
}

var accountCache sync.Map // uint → accountState

func lookupAccount(db *gorm.DB, id uint) (accountState, bool) {
	if v, ok := accountCache.Load(id); ok {
		if s := v.(accountState); time.Since(s.loaded) < accountCacheTTL {
			return s, true
		}
	}
	var s accountState
	res := db.Raw(`SELECT status, group_id, COALESCE(zip, '') AS zip FROM memberlogin_members WHERE id = ?`, id).Scan(&s)
	if res.Error != nil || res.RowsAffected == 0 {
		accountCache.Delete(id)
		return s, false
	}
	s.loaded = time.Now()
	accountCache.Store(id, s)
	return s, true
}

// InvalidateAccount drops the cached state of an account whose status,
// role or clinic just changed.
func InvalidateAccount(id uint) {
	accountCache.Delete(id)
}
