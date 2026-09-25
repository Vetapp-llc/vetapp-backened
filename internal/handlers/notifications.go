package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vetapp-backend/internal/services"

	"golang.org/x/sync/semaphore"
	"gorm.io/gorm"
)

// smsConcurrency caps how many SMS sends we do in parallel. 10 is
// conservative — SMSOffice can handle more, but this protects our own
// goroutines + the database connection pool when the cron fires for a
// clinic with hundreds of pets.
const smsConcurrency = 10

// dispatchSMSBatch fans out an SMS send across `messages` with a
// bounded-concurrency worker pool. Returns (sent, errors).
//
// Without this, the cron job sent SMS strictly sequentially —
// 200 reminders × ~1s upstream latency = ~3 minutes blocked. With
// concurrency 10 the same batch finishes in ~20s.
func dispatchSMSBatch(svc *services.SMSService, messages []struct{ Phone, Body string }) (int32, int32) {
	if len(messages) == 0 {
		return 0, 0
	}
	ctx := context.Background()
	sem := semaphore.NewWeighted(smsConcurrency)
	var sent, errs int32
	var wg sync.WaitGroup
	for _, m := range messages {
		if err := sem.Acquire(ctx, 1); err != nil {
			atomic.AddInt32(&errs, 1)
			continue
		}
		wg.Add(1)
		go func(phone, body string) {
			defer wg.Done()
			defer sem.Release(1)
			if err := svc.Send(phone, body); err != nil {
				atomic.AddInt32(&errs, 1)
				return
			}
			atomic.AddInt32(&sent, 1)
		}(m.Phone, m.Body)
	}
	wg.Wait()
	return sent, errs
}

// NotificationHandler handles SMS notification endpoints.
type NotificationHandler struct {
	db         *gorm.DB
	smsService *services.SMSService
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(db *gorm.DB, smsService *services.SMSService) *NotificationHandler {
	return &NotificationHandler{db: db, smsService: smsService}
}

// --- Reminder kinds ---
//
// The three messages of the PHP admin page sms/index.php, with its
// wording. PHP sent them only when an admin clicked each link; here they
// are sent from the admin portal (POST /notifications/sms/reminders) or,
// if SMS_DAILY_AT is set, once a day (see StartSMSScheduler).
//
// Selection, fixed where PHP was wrong:
//   - expired:   pets whose subscription ends today (pets.birth2 = today)
//   - birthday:  active pets born on this day. PHP compared the full date
//     of birth to today, which only ever matched pets born today; the
//     birthday lives in pets.happy as "MM-DD", or in pets.date.
//   - procedure: active pets with a procedure or booked appointment due in
//     three days (PHP checked procedures only).
//
// Each phone number receives a kind's message at most once per run,
// however many pets or procedures it matches.

const (
	smsExpired   = "expired"
	smsBirthday  = "birthday"
	smsProcedure = "procedure"
)

var smsKinds = []string{smsExpired, smsBirthday, smsProcedure}

var smsTexts = map[string]string{
	smsExpired:   "შეგახსენებთ, რომ თქვენი შინაური ცხოველის საწევრო პაკეტის ვადა ამოიწურა, გთხოვთ განაახლოთ.\nსიყვარულით Vetapp-ი 💙",
	smsBirthday:  "Vetapp-ი გილოცავთ თქვენი შინაური ცხოველის დაბადების დღეს. გისურვებთ ბედნიერ თანაცხოვრებას 💙",
	smsProcedure: "შეგახსენებთ, რომ თქვენს შინაურ ცხოველს უწევს გეგმიური პროცედურა(ები). დეტალები შეგიძლიათ იხილოთ Vetapp-ის აპლიკაციაში.\nსიყვარულით Vetapp-ი 💙",
}

// reminderPhones returns the de-duplicated phone numbers for one kind on
// the given Georgian calendar day.
func reminderPhones(db *gorm.DB, kind string, today time.Time) []string {
	day := today.Format("2006-01-02")
	var phones []string
	switch kind {
	case smsExpired:
		db.Raw(`SELECT DISTINCT TRIM(phone) FROM pets
			WHERE birth2 = ? AND TRIM(COALESCE(phone, '')) <> ''`, day).Scan(&phones)
	case smsBirthday:
		mmdd := today.Format("01-02")
		db.Raw(`SELECT DISTINCT TRIM(phone) FROM pets
			WHERE status = '1' AND birth2 >= ? AND TRIM(COALESCE(phone, '')) <> ''
			  AND (happy = ? OR (COALESCE(happy, '') = '' AND RIGHT(date, 5) = ? AND date ~ '^[0-9]{4}-'))`,
			day, mmdd, mmdd).Scan(&phones)
	case smsProcedure:
		due := today.AddDate(0, 0, 3).Format("2006-01-02")
		db.Raw(`SELECT DISTINCT TRIM(p.phone) FROM pets p
			WHERE p.status = '1' AND p.birth2 >= ? AND TRIM(COALESCE(p.phone, '')) <> ''
			  AND p.id::text IN (
			        SELECT uuid FROM vaccination WHERE date2 = ?
			        UNION SELECT uuid FROM operationdate WHERE date2 = ?)`,
			day, due, due).Scan(&phones)
	}
	return phones
}

// --- Response types ---

// ReminderPreview is how many people each kind would reach today.
type ReminderPreview struct {
	Date      string            `json:"date" validate:"required"`
	Kinds     map[string]int    `json:"kinds" validate:"required"`
	SentToday map[string]int    `json:"sent_today" validate:"required"` // kinds already sent today
	Texts     map[string]string `json:"texts" validate:"required"`
}

// PreviewReminders counts today's recipients per kind without sending.
// @Summary Preview today's SMS reminders
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ReminderPreview
// @Router /notifications/sms/reminders [get]
func (h *NotificationHandler) PreviewReminders(w http.ResponseWriter, r *http.Request) {
	now := time.Now().In(georgia)
	out := ReminderPreview{Date: now.Format("2006-01-02"), Kinds: map[string]int{}, SentToday: map[string]int{}, Texts: smsTexts}
	for _, k := range smsKinds {
		out.Kinds[k] = len(reminderPhones(h.db, k, now))
	}
	var runs []struct {
		Kind string
		Sent int
	}
	h.db.Raw(`SELECT kind, sent FROM sms_runs WHERE run_date = ?`, out.Date).Scan(&runs)
	for _, run := range runs {
		out.SentToday[run.Kind] = run.Sent
	}
	writeJSON(w, http.StatusOK, out)
}

// ReminderResult is the result of sending reminders.
type ReminderResult struct {
	Expired    int      `json:"expired_sent" validate:"required"`
	Birthdays  int      `json:"birthdays_sent" validate:"required"`
	Procedures int      `json:"procedures_sent" validate:"required"`
	Errors     int      `json:"errors" validate:"required"`
	Skipped    []string `json:"skipped" validate:"required"` // kinds already sent today
}

// SendReminders sends today's reminders. `kinds` (comma-separated)
// limits which are sent; each kind goes out at most once per day.
// @Summary Send today's SMS reminders
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Param kinds query string false "expired,birthday,procedure (default all)"
// @Success 200 {object} ReminderResult
// @Router /notifications/sms/reminders [post]
func (h *NotificationHandler) SendReminders(w http.ResponseWriter, r *http.Request) {
	kinds := smsKinds
	if q := strings.TrimSpace(r.URL.Query().Get("kinds")); q != "" {
		kinds = nil
		for _, k := range strings.Split(q, ",") {
			k = strings.TrimSpace(k)
			if smsTexts[k] == "" {
				writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "unknown kind " + k})
				return
			}
			kinds = append(kinds, k)
		}
	}
	writeJSON(w, http.StatusOK, runReminders(h.db, h.smsService, kinds, time.Now().In(georgia)))
}

// runReminders sends each kind once for the day. The sms_runs row is
// claimed before sending, so a concurrent or repeated run skips the kind
// instead of texting the same people again.
func runReminders(db *gorm.DB, svc *services.SMSService, kinds []string, now time.Time) ReminderResult {
	day := now.Format("2006-01-02")
	res := ReminderResult{Skipped: []string{}}
	for _, kind := range kinds {
		claim := db.Exec(`INSERT INTO sms_runs (run_date, kind) VALUES (?, ?) ON CONFLICT DO NOTHING`, day, kind)
		if claim.Error != nil || claim.RowsAffected == 0 {
			res.Skipped = append(res.Skipped, kind)
			continue
		}
		phones := reminderPhones(db, kind, now)
		msgs := make([]struct{ Phone, Body string }, len(phones))
		for i, p := range phones {
			msgs[i] = struct{ Phone, Body string }{Phone: p, Body: smsTexts[kind]}
		}
		sent, errs := dispatchSMSBatch(svc, msgs)
		db.Exec(`UPDATE sms_runs SET recipients = ?, sent = ?, errors = ? WHERE run_date = ? AND kind = ?`,
			len(phones), sent, errs, day, kind)
		slog.Info("sms_reminders_sent", "kind", kind, "recipients", len(phones), "sent", sent, "errors", errs)
		res.Errors += int(errs)
		switch kind {
		case smsExpired:
			res.Expired = int(sent)
		case smsBirthday:
			res.Birthdays = int(sent)
		case smsProcedure:
			res.Procedures = int(sent)
		}
	}
	return res
}

// StartSMSScheduler sends the day's reminders at a fixed Georgian time
// when SMS_DAILY_AT ("HH:MM") is set. Off by default: PHP never sent
// automatically, and each run costs money, so turning it on is a
// deliberate choice. Safe with several instances — sms_runs lets only
// one send per kind per day.
func StartSMSScheduler(ctx context.Context, db *gorm.DB, svc *services.SMSService, at string) {
	if at == "" {
		return
	}
	t, err := time.Parse("15:04", at)
	if err != nil {
		slog.Error("sms_scheduler_disabled", "reason", "SMS_DAILY_AT must be HH:MM", "value", at)
		return
	}
	go func() {
		for {
			now := time.Now().In(georgia)
			next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, georgia)
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			slog.Info("sms_scheduler_next_run", "at", next.Format(time.RFC3339))
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(next)):
				runReminders(db, svc, smsKinds, time.Now().In(georgia))
			}
		}
	}()
}
