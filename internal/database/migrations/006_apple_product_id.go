package migrations

import "gorm.io/gorm"

// 006 — add apple_product_id to mprice.
//
// Backs the Apple IAP product-ID guard added in
// handlers/subscriptions.go::AppleVerify. Each row should be backfilled
// with the App Store Connect product identifier matching the package
// (e.g. com.vetapp.subscription.monthly). Until backfilled the field
// stays empty and the receipt's productId is not asserted — see the
// "fail-open during rollout" comment on the Package struct.
func init() {
	Register(Migration{
		ID: "006_add_apple_product_id_to_packages",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
				ALTER TABLE mprice
				ADD COLUMN IF NOT EXISTS apple_product_id TEXT NULL;
			`).Error
		},
		Down: func(db *gorm.DB) error {
			return db.Exec(`ALTER TABLE mprice DROP COLUMN IF EXISTS apple_product_id;`).Error
		},
	})
}
