package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"time"

	"vetapp-backend/internal/middleware"
)

var (
	expoToken      = regexp.MustCompile(`^(Expo|Exponent)PushToken\[[a-zA-Z0-9_-]{10,128}\]$`)
	expoPushClient = &http.Client{Timeout: 4 * time.Second}
)

type ownerPushDevice struct {
	Token string `json:"token" validate:"required"`
}

func (h *OwnerPortalHandler) UpdatePushDevice(w http.ResponseWriter, r *http.Request) {
	var device ownerPushDevice
	if err := decodeAndValidate(r, &device); err != nil || !expoToken.MatchString(device.Token) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid push token"})
		return
	}
	userID := middleware.GetClaims(r).UserID
	var err error
	if r.Method == http.MethodDelete {
		err = h.db.Exec("DELETE FROM owner_push_devices WHERE token = ? AND user_id = ?", device.Token, userID).Error
	} else {
		err = h.db.Exec(`INSERT INTO owner_push_devices (token, user_id) VALUES (?, ?)
			ON CONFLICT (token) DO UPDATE SET user_id = EXCLUDED.user_id, updated_at = NOW()`, device.Token, userID).Error
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not update push device"})
		return
	}
	writeJSON(w, http.StatusOK, MessageResponse{Message: "ok"})
}

// Inbox requests are stored before sending. The push has generic text so a
// stale device token cannot reveal pet details, owners' names or IDs.
func (h *OwnerPortalHandler) notifyTransferRecipient(r *http.Request, userID uint) {
	var tokens []string
	if err := h.db.Table("owner_push_devices").Where("user_id = ?", userID).
		Order("updated_at DESC").Limit(20).Pluck("token", &tokens).Error; err != nil {
		middleware.RequestLogger(r).Warn("transfer_push_device_lookup_failed")
		return
	}
	if len(tokens) == 0 {
		return
	}
	body, err := json.Marshal(map[string]interface{}{
		"to": tokens, "title": "VetApp", "body": "ცხოველის გადაცემის ახალი მოთხოვნა გაქვთ.",
		"sound": "default", "channelId": "default", "ttl": 86400,
		"data": map[string]string{"type": "pet_transfer"},
	})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(context.WithoutCancel(r.Context()), http.MethodPost,
		"https://exp.host/--/api/v2/push/send", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key := os.Getenv("EXPO_PUSH_ACCESS_TOKEN"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := expoPushClient.Do(req)
	if err != nil {
		middleware.RequestLogger(r).Warn("transfer_push_send_failed")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		middleware.RequestLogger(r).Warn("transfer_push_rejected", "status", res.StatusCode)
		return
	}
	var tickets struct {
		Data []struct {
			Status  string
			Details struct{ Error string }
		}
	}
	if err := json.NewDecoder(res.Body).Decode(&tickets); err != nil || len(tickets.Data) != len(tokens) {
		middleware.RequestLogger(r).Warn("transfer_push_invalid_response")
		return
	}
	for i, ticket := range tickets.Data {
		if ticket.Status == "ok" {
			continue
		}
		middleware.RequestLogger(r).Warn("transfer_push_ticket_failed", "code", ticket.Details.Error)
		if ticket.Details.Error == "DeviceNotRegistered" {
			h.db.Exec("DELETE FROM owner_push_devices WHERE token = ? AND user_id = ?", tokens[i], userID)
		}
	}
}
