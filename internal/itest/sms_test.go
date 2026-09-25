package itest

import (
	"net/http"
	"sort"
	"testing"
	"time"

	"vetapp-backend/internal/models"
)

func TestSMSRemindersSelectDedupeAndSendOnce(t *testing.T) {
	reset(t)
	georgia := time.FixedZone("GE", 4*3600)
	now := time.Now().In(georgia)
	today := now.Format("2006-01-02")
	future := now.AddDate(1, 0, 0).Format("2006-01-02")
	due := now.AddDate(0, 0, 3).Format("2006-01-02")
	mmdd := now.Format("01-02")

	pets := []models.Pet{
		{ID: 1, Name: "ended", Phone: "555000001", Birth2: today, Status: 1},
		// birthday via happy, and a second pet of the same owner: one SMS.
		{ID: 2, Name: "bday", Phone: "555000002", Status: 1, Birth2: future},
		{ID: 3, Name: "bday2", Phone: "555000002", Status: 1, Birth2: future},
		// birthday via date of birth; and an inactive pet that must be skipped.
		{ID: 4, Name: "bday3", Phone: "555000004", Status: 1, Birth2: future, Date: "2019-" + mmdd},
		{ID: 5, Name: "inactive", Phone: "555000005", Status: 2, Birth2: "", Date: "2019-" + mmdd},
		// due procedure + due appointment for one pet: one SMS.
		{ID: 6, Name: "due", Phone: "555000006", Status: 1, Birth2: future},
	}
	for _, p := range pets {
		must(t, db.Create(&p).Error)
	}
	must(t, db.Exec(`UPDATE pets SET happy = ? WHERE id IN (2, 3)`, mmdd).Error)
	insertProc(t, models.Procedure{UUID: "6", TP: 1, Date2: due})
	must(t, db.Exec(`INSERT INTO operationdate (uuid, date2, sk) VALUES ('6', ?, ?)`, due, clinicA).Error)

	r := call(t, admin, "GET", "/api/notifications/sms/reminders", nil)
	expect(t, r, http.StatusOK)
	var preview struct{ Kinds map[string]int }
	r.json(t, &preview)
	if preview.Kinds["expired"] != 1 || preview.Kinds["birthday"] != 2 || preview.Kinds["procedure"] != 1 {
		t.Fatalf("preview = %+v", preview.Kinds)
	}

	r = call(t, admin, "POST", "/api/notifications/sms/reminders", nil)
	expect(t, r, http.StatusOK)
	smsMu.Lock()
	got := append([]string(nil), smsSent...)
	smsMu.Unlock()
	sort.Strings(got)
	want := []string{"555000001", "555000002", "555000004", "555000006"}
	if len(got) != len(want) {
		t.Fatalf("sent to %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sent to %v, want %v", got, want)
		}
	}

	// A second run the same day sends nothing.
	r = call(t, admin, "POST", "/api/notifications/sms/reminders", nil)
	var res struct{ Skipped []string }
	r.json(t, &res)
	if len(res.Skipped) != 3 {
		t.Errorf("second run skipped %v, want all three kinds", res.Skipped)
	}
	smsMu.Lock()
	n := len(smsSent)
	smsMu.Unlock()
	if n != 4 {
		t.Errorf("second run sent more messages: %d total", n)
	}
	expect(t, call(t, vetA, "POST", "/api/notifications/sms/reminders", nil), http.StatusForbidden)
}
