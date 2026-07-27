package handlers

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

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

// --- Response types ---

// ReminderResult is the result of sending reminders.
type ReminderResult struct {
	Expired    int `json:"expired_sent" validate:"required"`
	Birthdays  int `json:"birthdays_sent" validate:"required"`
	Procedures int `json:"procedures_sent" validate:"required"`
	Errors     int `json:"errors" validate:"required"`
}

// --- Handlers ---

// SendReminders sends SMS reminders for expired packages, birthdays, and upcoming procedures.
// @Summary Send SMS reminders
// @Tags notifications
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ReminderResult
// @Failure 500 {object} ErrorResponse
// @Router /notifications/sms/reminders [post]
func (h *NotificationHandler) SendReminders(w http.ResponseWriter, r *http.Request) {
	var result ReminderResult
	type sms struct{ Phone, Body string }

	type phoneRow struct {
		Phone string
		Name  string
	}

	// 1. Expired packages
	var expired []phoneRow
	h.db.Raw(`SELECT p.phone, p.name FROM pets p
		WHERE p.birth2 = CURRENT_DATE::text
		AND TRIM(COALESCE(p.phone,'')) != ''`).Scan(&expired)

	expiredMsgs := make([]sms, 0, len(expired))
	for _, row := range expired {
		expiredMsgs = append(expiredMsgs, sms{
			Phone: row.Phone,
			Body:  fmt.Sprintf("VetApp: %s-ს პაკეტი ამოიწურა. გთხოვთ განაახლოთ.", row.Name),
		})
	}

	// 2. Birthdays
	var birthdays []phoneRow
	h.db.Raw(`SELECT p.phone, p.name FROM pets p
		WHERE SUBSTRING(p.happy FROM 6) = TO_CHAR(CURRENT_DATE, 'MM-DD')
		AND p.status = 1
		AND p.birth2 >= CURRENT_DATE::text
		AND TRIM(COALESCE(p.phone,'')) != ''`).Scan(&birthdays)

	birthdayMsgs := make([]sms, 0, len(birthdays))
	for _, row := range birthdays {
		birthdayMsgs = append(birthdayMsgs, sms{
			Phone: row.Phone,
			Body:  fmt.Sprintf("VetApp: გილოცავთ %s-ს დაბადების დღეს! 🎂", row.Name),
		})
	}

	// 3. Procedure reminders (3 days out)
	var reminders []struct {
		Phone  string
		Name   string
		TPName string
	}
	h.db.Raw(`SELECT p.phone, p.name, v.tpname
		FROM vaccination v
		JOIN pets p ON v.uuid = p.id::text
		WHERE v.date2 = (CURRENT_DATE + INTERVAL '3 days')::text
		AND CAST(v.date3 AS int) > 2
		AND p.status = 1
		AND p.birth2 >= CURRENT_DATE::text
		AND TRIM(COALESCE(p.phone,'')) != ''`).Scan(&reminders)

	reminderMsgs := make([]sms, 0, len(reminders))
	for _, row := range reminders {
		reminderMsgs = append(reminderMsgs, sms{
			Phone: row.Phone,
			Body:  fmt.Sprintf("VetApp: %s-ს %s 3 დღეში ესაჭიროება. გთხოვთ დაგვიკავშირდეთ.", row.Name, row.TPName),
		})
	}

	// Fan out — sends in parallel within the per-batch concurrency cap.
	// dispatchSMSBatch's signature uses a struct type with the same shape.
	convert := func(in []sms) []struct{ Phone, Body string } {
		out := make([]struct{ Phone, Body string }, len(in))
		for i, m := range in {
			out[i] = struct{ Phone, Body string }{Phone: m.Phone, Body: m.Body}
		}
		return out
	}
	sentE, errE := dispatchSMSBatch(h.smsService, convert(expiredMsgs))
	sentB, errB := dispatchSMSBatch(h.smsService, convert(birthdayMsgs))
	sentP, errP := dispatchSMSBatch(h.smsService, convert(reminderMsgs))

	result.Expired = int(sentE)
	result.Birthdays = int(sentB)
	result.Procedures = int(sentP)
	result.Errors = int(errE + errB + errP)

	writeJSON(w, http.StatusOK, result)
}
