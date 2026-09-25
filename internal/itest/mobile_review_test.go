package itest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"vetapp-backend/internal/models"
)

// registerOTP writes an unexpired register code for phone, as /otp/send
// would, without sending an SMS.
func registerOTP(t *testing.T, phone string) (uint, string) {
	t.Helper()
	o := models.OTP{Phone: phone, Code: "123456", Type: "register", ExpiresAt: time.Now().Add(5 * time.Minute), CreatedAt: time.Now()}
	must(t, db.Create(&o).Error)
	return o.ID, o.Code
}

func errCode(t *testing.T, r resp) string {
	t.Helper()
	var e struct{ Code string }
	json.Unmarshal(r.Body, &e)
	return e.Code
}

// Mobile review: app builds up to 1.0 sent the surname as last_name and
// the personal ID as idNumber (and otpId as a string). The ID must land
// in last_name and the surname in the display name.
func TestRegisterLegacyAppShapeStoresPersonalID(t *testing.T) {
	reset(t)
	id, code := registerOTP(t, "555111222")
	r := call(t, 0, "POST", "/api/auth/register", map[string]interface{}{
		"first_name": "გიორგი", "last_name": "ბერიძე", "idNumber": "61001012345", "email": "Legacy@Test.ge",
		"password": "pw", "phone": "+995555111222", "otpId": fmt.Sprint(id), "code": code,
	})
	expect(t, r, http.StatusCreated)
	var u models.User
	must(t, db.Where("email = ?", "Legacy@Test.ge").First(&u).Error)
	if u.LastName != "61001012345" || u.FirstName != "გიორგი ბერიძე" || u.Phone != "555111222" {
		t.Fatalf("stored last_name=%q first_name=%q phone=%q", u.LastName, u.FirstName, u.Phone)
	}
	// Another person with the same surname is not blocked any more.
	id, code = registerOTP(t, "555111223")
	expect(t, call(t, 0, "POST", "/api/auth/register", map[string]interface{}{
		"first_name": "ნინო", "last_name": "ბერიძე", "idNumber": "61001012346", "email": "nino@test.ge",
		"password": "pw", "phone": "555111223", "otpId": fmt.Sprint(id), "code": code,
	}), http.StatusCreated)
	// The email check ignores case.
	id, code = registerOTP(t, "555111224")
	r = call(t, 0, "POST", "/api/auth/register", map[string]interface{}{
		"first_name": "x", "last_name": "61001012347", "email": "legacy@test.ge", "password": "pw",
		"phone": "555111224", "otp_id": id, "otp_code": code,
	})
	expect(t, r, http.StatusConflict)
	if c := errCode(t, r); c != "email_taken" {
		t.Fatalf("code %q, want email_taken", c)
	}
	// Login ignores case too.
	expect(t, call(t, 0, "POST", "/api/auth/login", map[string]string{"email": "legacy@test.GE", "password": "pw"}), http.StatusOK)
}

// Mobile review: the register code was never checked.
func TestRegisterRequiresTheCodeSentToThatPhone(t *testing.T) {
	reset(t)
	body := func(phone string, id uint, code string) map[string]interface{} {
		return map[string]interface{}{"first_name": "x", "last_name": "61001019999", "email": "otp@test.ge",
			"password": "pw", "phone": phone, "otp_id": id, "otp_code": code}
	}
	r := call(t, 0, "POST", "/api/auth/register", body("555222333", 0, ""))
	expect(t, r, http.StatusUnauthorized)
	id, _ := registerOTP(t, "555222333")
	r = call(t, 0, "POST", "/api/auth/register", body("555222333", id, "000000"))
	expect(t, r, http.StatusUnauthorized)
	if c := errCode(t, r); c != "otp_invalid" {
		t.Fatalf("wrong code: %q", c)
	}
	// A code sent to another number does not prove this one.
	expect(t, call(t, 0, "POST", "/api/auth/register", body("555222334", id, "123456")), http.StatusUnauthorized)
	expect(t, call(t, 0, "POST", "/api/auth/register", body("555 22 23 33", id, "123456")), http.StatusCreated)
	// Used up.
	r = call(t, 0, "POST", "/api/auth/register", map[string]interface{}{"first_name": "x", "last_name": "61001018888",
		"email": "otp2@test.ge", "password": "pw", "phone": "555222333", "otp_id": id, "otp_code": "123456"})
	expect(t, r, http.StatusUnauthorized)
	if c := errCode(t, r); c != "otp_expired" {
		t.Fatalf("reused code: %q", c)
	}
	// Bad personal ID and phone are refused before anything else.
	r = call(t, 0, "POST", "/api/auth/register", map[string]interface{}{"first_name": "x", "last_name": "ab",
		"email": "otp3@test.ge", "password": "pw", "phone": "555222333"})
	if r.Code != http.StatusBadRequest || errCode(t, r) != "invalid_personal_id" {
		t.Fatalf("bad id: %d %s", r.Code, r.Body)
	}
}

// Mobile review: /otp/verify used the code up, so the reset that follows
// always failed; and the account is found whatever format its phone has.
func TestPasswordResetAfterVerify(t *testing.T) {
	reset(t)
	must(t, db.Model(&models.User{}).Where("id = ?", owner).Updates(map[string]interface{}{"phone": "+995 599 000 000", "email": "owner@test.ge"}).Error)

	r := call(t, 0, "POST", "/api/auth/otp/send", map[string]string{"phone": "599000000", "type": "recovery"})
	expect(t, r, http.StatusOK)
	var sent struct {
		OTPID uint `json:"otp_id"`
	}
	r.json(t, &sent)
	var code string
	db.Raw(`SELECT code FROM otp_codes WHERE id = ?`, sent.OTPID).Scan(&code)
	smsMu.Lock()
	dest := append([]string(nil), smsSent...)
	smsMu.Unlock()
	if len(dest) != 1 || dest[0] != "599000000" {
		t.Fatalf("sms destinations %v", dest)
	}

	expect(t, call(t, 0, "POST", "/api/auth/otp/verify", map[string]interface{}{"otp_id": sent.OTPID, "code": code}), http.StatusOK)
	reset := map[string]interface{}{"otp_id": sent.OTPID, "otp_code": code, "new_password": "new-secret-1"}
	expect(t, call(t, 0, "POST", "/api/auth/password-reset", reset), http.StatusOK)
	r = call(t, 0, "POST", "/api/auth/password-reset", reset)
	if r.Code != http.StatusUnauthorized || errCode(t, r) != "otp_expired" {
		t.Fatalf("second reset: %d %s", r.Code, r.Body)
	}
	expect(t, call(t, 0, "POST", "/api/auth/login", map[string]string{"email": "owner@test.ge", "password": "new-secret-1"}), http.StatusOK)

	// Unknown numbers and taken emails are reported before an SMS is sent.
	r = call(t, 0, "POST", "/api/auth/otp/send", map[string]string{"phone": "+995 577 12 34 56", "type": "recovery"})
	if r.Code != http.StatusNotFound || errCode(t, r) != "user_not_found" {
		t.Fatalf("unknown phone: %d %s", r.Code, r.Body)
	}
	r = call(t, 0, "POST", "/api/auth/otp/send", map[string]string{"phone": "577123457", "type": "register", "email": "OWNER@test.ge"})
	if r.Code != http.StatusConflict || errCode(t, r) != "email_taken" {
		t.Fatalf("taken email: %d %s", r.Code, r.Body)
	}
	// Resending within a minute is refused with a wait time.
	r = call(t, 0, "POST", "/api/auth/otp/send", map[string]string{"phone": "599000000", "type": "recovery"})
	var rl struct {
		Code       string
		RetryAfter int `json:"retry_after"`
	}
	r.json(t, &rl)
	if r.Code != http.StatusTooManyRequests || rl.Code != "rate_limited" || rl.RetryAfter <= 0 {
		t.Fatalf("resend: %d %s", r.Code, r.Body)
	}
	smsMu.Lock()
	n := len(smsSent)
	smsMu.Unlock()
	if n != 1 {
		t.Fatalf("%d SMS sent, want 1", n)
	}
}

func TestProfilePhoneIsNormalized(t *testing.T) {
	reset(t)
	expect(t, call(t, owner, "PUT", "/api/auth/me", map[string]string{"phone": "995 599 11 22 33"}), http.StatusOK)
	var u models.User
	must(t, db.First(&u, owner).Error)
	if u.Phone != "599112233" {
		t.Fatalf("phone stored as %q", u.Phone)
	}
	r := call(t, owner, "PUT", "/api/auth/me", map[string]string{"phone": "123"})
	if r.Code != http.StatusBadRequest || errCode(t, r) != "invalid_phone" {
		t.Fatalf("bad phone: %d %s", r.Code, r.Body)
	}
}

// Mobile review: public pages were readable by walking pet ids, and PINs
// collided (pets.code holds PHP's 1313 for many pets).
func TestPublicPetPagesNeedAShareToken(t *testing.T) {
	reset(t)
	insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 108, SK: clinicA, Diagn: "secret diagnosis", Date: "2026-09-01"})
	must(t, db.Model(&models.Pet{}).Where("id IN ?", []uint{petA, petB}).Update("code", "1313").Error)

	pa, pb := fmt.Sprint(petA), fmt.Sprint(petB)
	expect(t, call(t, 0, "GET", "/api/public/pets/"+pa, nil), http.StatusNotFound)
	expect(t, call(t, 0, "GET", "/api/public/pets/"+pa+"/procedures", nil), http.StatusNotFound)
	expect(t, call(t, 0, "GET", "/api/public/pets/code/1313", nil), http.StatusNotFound)

	// QR link token.
	var link struct {
		Token     string
		ExpiresAt time.Time `json:"expires_at"`
	}
	r := call(t, owner, "GET", "/api/owner/pets/"+pa+"/share-link", nil)
	expect(t, r, http.StatusOK)
	r.json(t, &link)
	if len(link.Token) < 30 || time.Until(link.ExpiresAt) < 6*24*time.Hour {
		t.Fatalf("link %+v", link)
	}
	var again struct{ Token string }
	call(t, owner, "GET", "/api/owner/pets/"+pa+"/share-link", nil).json(t, &again)
	if again.Token != link.Token {
		t.Fatal("share link changed between two opens of the QR screen")
	}
	expect(t, call(t, 0, "GET", "/api/public/pets/"+pa+"?s="+link.Token, nil), http.StatusOK)
	r = call(t, 0, "GET", "/api/public/pets/"+pa+"/procedures?s="+link.Token, nil)
	expect(t, r, http.StatusOK)
	if !strings.Contains(string(r.Body), "secret diagnosis") {
		t.Fatalf("history missing: %s", r.Body)
	}
	// The token opens only its own pet.
	expect(t, call(t, 0, "GET", "/api/public/pets/"+pb+"?s="+link.Token, nil), http.StatusNotFound)

	// PIN: 6 digits, unique, and it unlocks the history via share_token.
	var pin struct {
		Code string
	}
	r = call(t, owner, "GET", "/api/owner/pets/"+pb+"/code", nil)
	expect(t, r, http.StatusOK)
	r.json(t, &pin)
	if len(pin.Code) != 6 {
		t.Fatalf("pin %q", pin.Code)
	}
	var prof struct {
		Pet        struct{ ID string }
		ShareToken string `json:"share_token"`
	}
	r = call(t, 0, "GET", "/api/public/pets/code/"+pin.Code, nil)
	expect(t, r, http.StatusOK)
	r.json(t, &prof)
	if prof.Pet.ID != pb || prof.ShareToken == "" {
		t.Fatalf("pin lookup %s", r.Body)
	}
	expect(t, call(t, 0, "GET", "/api/public/pets/"+pb+"/procedures?s="+prof.ShareToken, nil), http.StatusOK)
	// A PIN is not itself a key to the pages (it could be guessed there
	// without the lookup's rate limit).
	expect(t, call(t, 0, "GET", "/api/public/pets/"+pb+"/procedures?s="+pin.Code, nil), http.StatusNotFound)
	// A new PIN replaces the old one.
	var pin2 struct{ Code string }
	call(t, owner, "GET", "/api/owner/pets/"+pb+"/code", nil).json(t, &pin2)
	if pin2.Code != pin.Code {
		expect(t, call(t, 0, "GET", "/api/public/pets/code/"+pin.Code, nil), http.StatusNotFound)
	}
	// Expired tokens stop working.
	must(t, db.Exec(`UPDATE pet_share_tokens SET expires_at = NOW() - INTERVAL '1 minute'`).Error)
	expect(t, call(t, 0, "GET", "/api/public/pets/"+pa+"?s="+link.Token, nil), http.StatusNotFound)
	expect(t, call(t, 0, "GET", "/api/public/pets/code/"+pin2.Code, nil), http.StatusNotFound)
	expect(t, call(t, 0, "GET", "/api/public/pets/"+pb+"?s="+prof.ShareToken, nil), http.StatusNotFound)
	// Another owner's pet id is not the caller's to share.
	expect(t, call(t, vetA, "GET", "/api/owner/pets/"+pa+"/share-link", nil), http.StatusForbidden)
}

// Owner-side fixes from the mobile review.
func TestOwnerDeleteRuleAndTrimmedPetIDs(t *testing.T) {
	reset(t)
	// A record with vetname "0" is shown as owner-added, so it must be
	// deletable too.
	p := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 108, VetName: "0", Date: "2026-09-01"})
	expect(t, call(t, owner, "DELETE", fmt.Sprintf("/api/owner/pets/%d/procedures/%d", petA, p.ID), nil), http.StatusOK)
	v := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 108, VetName: fmt.Sprint(vetA), Date: "2026-09-01"})
	expect(t, call(t, owner, "DELETE", fmt.Sprintf("/api/owner/pets/%d/procedures/%d", petA, v.ID), nil), http.StatusForbidden)

	// Pets whose stored owner ID has stray spaces still belong to the owner.
	must(t, db.Model(&models.Pet{}).Where("id = ?", petB).Update("uuid", " "+ownerID+" ").Error)
	r := call(t, owner, "GET", "/api/owner/pets", nil)
	expect(t, r, http.StatusOK)
	if !strings.Contains(string(r.Body), fmt.Sprintf(`"id":"%d"`, petB)) {
		t.Fatalf("pet with padded owner id missing: %s", r.Body)
	}
	expect(t, call(t, owner, "GET", fmt.Sprintf("/api/owner/pets/%d", petB), nil), http.StatusOK)
}

func TestPaymentReturnPage(t *testing.T) {
	for _, st := range []string{"success", "fail", ""} {
		r := call(t, 0, "GET", "/api/subscriptions/return?status="+st, nil)
		expect(t, r, http.StatusOK)
		if !strings.Contains(string(r.Body), "VetApp") {
			t.Fatalf("return page %q: %s", st, r.Body)
		}
	}
}

// Codex re-review: parallel wrong guesses could all pass the tries check.
func TestOTPAttemptBudgetHoldsUnderConcurrency(t *testing.T) {
	reset(t)
	id, _ := registerOTP(t, "555333444")
	codes := raceN(t, 12, func() resp {
		return call(t, 0, "POST", "/api/auth/otp/verify", map[string]interface{}{"otp_id": id, "code": "000000"})
	})
	var tries int
	db.Raw(`SELECT tries FROM otp_codes WHERE id = ?`, id).Scan(&tries)
	if tries > 8 {
		t.Fatalf("tries = %d after 12 parallel guesses, budget is 8 (%v)", tries, codes)
	}
	// Budget spent: even the right code is refused now.
	r := call(t, 0, "POST", "/api/auth/otp/verify", map[string]interface{}{"otp_id": id, "code": "123456"})
	if r.Code != http.StatusUnauthorized || errCode(t, r) != "otp_expired" {
		t.Fatalf("right code after lockout: %d %s", r.Code, r.Body)
	}
}

func TestLegacyPhoneFormatsMatchOnRecovery(t *testing.T) {
	for _, stored := range []string{"0599000000", "00995599000000", "+995-599-00-00-00"} {
		reset(t)
		must(t, db.Model(&models.User{}).Where("id = ?", owner).Update("phone", stored).Error)
		var n int64
		db.Model(&models.User{}).Where(`(phone = ? OR regexp_replace(regexp_replace(phone, '[^0-9+]', '', 'g'),
	'^([+]|00){0,1}(995|0){0,1}(5[0-9]{8})$', '\3') = ?)`, "599000000", "599000000").Count(&n)
		if n != 1 {
			t.Fatalf("stored %q not matched", stored)
		}
	}
}
