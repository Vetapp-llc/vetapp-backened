package models

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// Package and Date must NOT reach the database: payments_ipay has no
// such columns, and including them made every insert fail with
// SQLSTATE 42703 (no purchase could be recorded, Apple IAP included).
func TestSubscriptionIgnoresAbsentColumns(t *testing.T) {
	s, err := schema.Parse(&Subscription{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}
	for _, name := range s.DBNames {
		if name == "package" || name == "date" {
			t.Errorf("column %q is persisted but does not exist on payments_ipay", name)
		}
	}
	// They must still exist as Go fields — checkout/callback pass them around.
	for _, f := range []string{"Package", "Date"} {
		if s.LookUpField(f) == nil {
			t.Errorf("field %s was removed; callers still reference it", f)
		}
	}
}
