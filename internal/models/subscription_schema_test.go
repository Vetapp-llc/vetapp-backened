package models

import "testing"

// Pins payments_ipay column names. The table is (id, pet_id, price,
// currency, ..., status, provider, receipt, product_id, order_id,
// trans_id) — it has no uuid/amount/date/package columns.
func TestSubscriptionColumns(t *testing.T) {
	assertColumns(t, &Subscription{}, map[string]string{
		"ID":        "id",
		"UUID":      "pet_id",
		"Amount":    "price",
		"Status":    "status",
		"OrderID":   "order_id",
		"TransID":   "trans_id",
		"Provider":  "provider",
		"Receipt":   "receipt",
		"ProductID": "product_id",
	})
}
