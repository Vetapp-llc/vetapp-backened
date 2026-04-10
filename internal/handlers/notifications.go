package handlers

import (
	"fmt"
	"net/http"

	"vetapp-backend/internal/services"

	"gorm.io/gorm"
)

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
	Expired    int `json:"expired_sent"`
	Birthdays  int `json:"birthdays_sent"`
	Procedures int `json:"procedures_sent"`
	Errors     int `json:"errors"`
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

	// 1. Expired packages: pets where birth2 = today
	type phoneRow struct {
		Phone string
		Name  string
	}

	var expired []phoneRow
	h.db.Raw(`SELECT p.phone, p.name FROM pets p
		WHERE p.birth2 = CURRENT_DATE::text
		AND TRIM(COALESCE(p.phone,'')) != ''`).Scan(&expired)

	for _, row := range expired {
		msg := fmt.Sprintf("VetApp: %s-ს პაკეტი ამოიწურა. გთხოვთ განაახლოთ.", row.Name)
		if err := h.smsService.Send(row.Phone, msg); err != nil {
			result.Errors++
		} else {
			result.Expired++
		}
	}

	// 2. Birthday greetings: pets where happy = today's MM-DD, active subscription
	var birthdays []phoneRow
	h.db.Raw(`SELECT p.phone, p.name FROM pets p
		WHERE SUBSTRING(p.happy FROM 6) = TO_CHAR(CURRENT_DATE, 'MM-DD')
		AND p.status = 1
		AND p.birth2 >= CURRENT_DATE::text
		AND TRIM(COALESCE(p.phone,'')) != ''`).Scan(&birthdays)

	for _, row := range birthdays {
		msg := fmt.Sprintf("VetApp: გილოცავთ %s-ს დაბადების დღეს! 🎂", row.Name)
		if err := h.smsService.Send(row.Phone, msg); err != nil {
			result.Errors++
		} else {
			result.Birthdays++
		}
	}

	// 3. Procedure reminders: vaccinations due in 3 days for active pets
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

	for _, row := range reminders {
		msg := fmt.Sprintf("VetApp: %s-ს %s 3 დღეში ესაჭიროება. გთხოვთ დაგვიკავშირდეთ.", row.Name, row.TPName)
		if err := h.smsService.Send(row.Phone, msg); err != nil {
			result.Errors++
		} else {
			result.Procedures++
		}
	}

	writeJSON(w, http.StatusOK, result)
}
