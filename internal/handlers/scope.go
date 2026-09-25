package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"gorm.io/gorm"
)

// Clinic scoping.
//
// Every clinic-owned table stores the clinic code in one column — `zip` on
// shop/paymethod/prices, `sk` on vaccination/eals/operationdate. A vet may
// only ever read or change rows whose clinic column equals the clinic in
// their token. Admins are not clinic-bound.
//
// The two helpers below are the only sanctioned way to apply that rule, so
// a handler cannot forget half of it (the ?clinic= override was once
// honoured for vets in six handlers, and by-id edits skipped the check
// entirely in five).

// resolveClinic returns the clinic a list/report request should read,
// honouring the `?clinic=...` override only for admins.
//
// Returns ok=false (with the response already written) when a non-admin
// tries to override. Callers should bail when ok=false.
func resolveClinic(w http.ResponseWriter, r *http.Request) (clinic string, ok bool) {
	claims := middleware.GetClaims(r)
	clinic = claims.Zip
	if override := r.URL.Query().Get("clinic"); override != "" && override != clinic {
		if claims.GroupID != models.RoleAdmin {
			writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "admin only"})
			return "", false
		}
		clinic = override
	}
	return clinic, true
}

// ownedByCaller restricts a query for a single row to the caller's clinic.
// column is the table's clinic column ("zip" or "sk"). Admins see every
// clinic. Use it for every by-id read, update and delete so that a row in
// another clinic is indistinguishable from a row that does not exist.
func ownedByCaller(db *gorm.DB, r *http.Request, column string) *gorm.DB {
	claims := middleware.GetClaims(r)
	if claims != nil && claims.GroupID == models.RoleAdmin {
		return db
	}
	zip := ""
	if claims != nil {
		zip = claims.Zip
	}
	// An empty zip must match nothing, not every row with an empty column.
	if zip == "" {
		return db.Where("1 = 0")
	}
	return db.Where(column+" = ?", zip)
}

// isAdmin reports whether the caller is a super admin.
func isAdmin(r *http.Request) bool {
	c := middleware.GetClaims(r)
	return c != nil && c.GroupID == models.RoleAdmin
}

// pickFields copies only the allowed keys out of a decoded JSON body.
// Handlers that accept partial updates as a map must never pass the raw
// map to GORM: it writes any column named in it, which let callers set a
// procedure's paid flag, reassign a pet to another owner or extend its
// subscription.
func pickFields(in map[string]interface{}, allowed ...string) map[string]interface{} {
	out := make(map[string]interface{}, len(allowed))
	for _, k := range allowed {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	return out
}

// canAccessPet reports whether the caller may see a pet's records.
//
// Pets are not owned by one clinic: any clinic may look up a pet by its
// owner's personal ID or its microchip (as PHP's search pages do) and
// treat it. What a clinic may NOT do is browse pets it has no connection
// to by stepping through sequential ids. So a clinic can open a pet if
// it is registered there, or if the clinic has already recorded a
// procedure for it. Admins can open any pet.
//
// The third way in is proof of an exact lookup: the caller names the
// owner's personal ID (`?owner_id=` on reads, `owner` in create bodies)
// and it matches the pet. That is what PHP's search pages required
// (vet/search2.php), and it lets a clinic treat a walk-in registered
// elsewhere. A bare pet id — sequential and guessable — never suffices.
func canAccessPet(db *gorm.DB, r *http.Request, petID string) bool {
	return canAccessPetWithProof(db, r, petID, r.URL.Query().Get("owner_id"))
}

// canAccessPetWithProof is canAccessPet with the owner-ID proof passed
// explicitly (for request bodies).
func canAccessPetWithProof(db *gorm.DB, r *http.Request, petID, ownerProof string) bool {
	claims := middleware.GetClaims(r)
	if claims == nil || petID == "" {
		return false
	}
	if claims.GroupID == models.RoleAdmin {
		return true
	}
	if claims.Zip == "" {
		return false
	}
	id, err := strconv.ParseUint(petID, 10, 32)
	if err != nil {
		return false
	}
	if proof := strings.TrimSpace(ownerProof); proof != "" {
		var n int64
		db.Model(&models.Pet{}).Where("id = ? AND TRIM(uuid) = ?", id, proof).Count(&n)
		if n > 0 {
			return true
		}
	}
	// Both probes are index lookups: pets_pkey, and the uuid prefix of
	// idx_vaccination_uuid_tp_date (a pet has at most a few hundred rows).
	var ok bool
	db.Raw(`SELECT EXISTS (SELECT 1 FROM pets WHERE id = ? AND vet = ?)
	           OR EXISTS (SELECT 1 FROM vaccination WHERE uuid = ? AND sk = ?)`,
		id, claims.Zip, strconv.FormatUint(id, 10), claims.Zip).Scan(&ok)
	return ok
}
