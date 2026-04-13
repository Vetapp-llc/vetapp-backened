package migrations

import "gorm.io/gorm"

func init() {
	Register(Migration{
		ID: "002_add_provider_to_payments",
		Up: func(db *gorm.DB) error {
			// Add provider column: "ipay" or "apple"
			if err := db.Exec(`ALTER TABLE payments_ipay ADD COLUMN IF NOT EXISTS provider VARCHAR(20) DEFAULT 'ipay'`).Error; err != nil {
				return err
			}
			// Add receipt column for Apple IAP receipt data
			if err := db.Exec(`ALTER TABLE payments_ipay ADD COLUMN IF NOT EXISTS receipt TEXT DEFAULT ''`).Error; err != nil {
				return err
			}
			// Add product_id column for Apple IAP product identifier
			if err := db.Exec(`ALTER TABLE payments_ipay ADD COLUMN IF NOT EXISTS product_id VARCHAR(100) DEFAULT ''`).Error; err != nil {
				return err
			}
			return nil
		},
		Down: func(db *gorm.DB) error {
			db.Exec(`ALTER TABLE payments_ipay DROP COLUMN IF EXISTS provider`)
			db.Exec(`ALTER TABLE payments_ipay DROP COLUMN IF EXISTS receipt`)
			db.Exec(`ALTER TABLE payments_ipay DROP COLUMN IF EXISTS product_id`)
			return nil
		},
	})
}
