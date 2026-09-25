package itest

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"vetapp-backend/internal/models"
)

// Twenty desks paying the same item at once: exactly one payment.
func TestConcurrentPaymentsChargeOnce(t *testing.T) {
	reset(t)
	p := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 1, SK: clinicA, Phone: "0", Price: "40", Date: "2026-09-24"})
	codes := raceN(t, 20, func() resp {
		return call(t, vetA, "POST", "/api/payments/record", map[string]interface{}{
			"uuid": fmt.Sprint(petA), "method": "card", "amount": "40", "procedure_ids": []uint{p.ID},
		})
	})
	if codes[http.StatusCreated] != 1 || codes[http.StatusConflict] != 19 {
		t.Fatalf("status counts = %v, want one 201 and nineteen 409", codes)
	}
	var n int64
	db.Model(&models.Payment{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d payment rows, want 1", n)
	}
}

// Twenty bookings of one vet's slot at once: exactly one booking.
func TestConcurrentBookingsTakeSlotOnce(t *testing.T) {
	reset(t)
	codes := raceN(t, 20, func() resp {
		return call(t, vetA, "POST", "/api/appointments", map[string]interface{}{
			"uuid": fmt.Sprint(petA), "date": "2026-11-02", "time": "11:00", "tpname": "ქირურგია",
		})
	})
	if codes[http.StatusCreated] != 1 || codes[http.StatusConflict] != 19 {
		t.Fatalf("status counts = %v, want one 201 and nineteen 409", codes)
	}
}

func raceN(t *testing.T, n int, fn func() resp) map[int]int {
	t.Helper()
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := fn()
			mu.Lock()
			codes[r.Code]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	return codes
}
