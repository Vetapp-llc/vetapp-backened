package migrations

import "gorm.io/gorm"

// 005 — make payments_ipay.order_id unique per provider.
//
// Fixes two issues:
//   1. iPay callback re-deliveries could create duplicate rows if the
//      first attempt landed before the conditional update was added.
//   2. Apple IAP duplicate-guard in handlers/subscriptions.go relied on
//      a sequential SELECT by order_id+provider — without the index
//      this is a seq scan on every receipt, and racy without a unique
//      constraint.
//
// `order_id` alone isn't unique because Apple transactionId and iPay
// shop_order_id come from different namespaces; (provider, order_id)
// is the right key.
func init() {
	Register(Migration{
		ID: "005_subscriptions_unique_order",
		Up: func(db *gorm.DB) error {
			// The legacy `payments_ipay` table came from a MySQL import
			// and may not have the `order_id` / `trans_id` columns the
			// Subscription model expects (older PHP rows used inline
			// shop_order_id-only). Add them if missing before creating
			// the unique index, otherwise the index call fails on fresh
			// DBs with the legacy schema.
			stmts := []string{
				`ALTER TABLE payments_ipay ADD COLUMN IF NOT EXISTS order_id TEXT NULL`,
				`ALTER TABLE payments_ipay ADD COLUMN IF NOT EXISTS trans_id TEXT NULL`,
				// Partial index — skip the historical NULL rows that
				// pre-date the column. New rows always have order_id set
				// via the Subscription model in handlers/subscriptions.go.
				`CREATE UNIQUE INDEX IF NOT EXISTS uq_payments_ipay_provider_order
					ON payments_ipay (provider, order_id)
					WHERE order_id IS NOT NULL`,
			}
			for _, s := range stmts {
				if err := db.Exec(s).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(db *gorm.DB) error {
			return db.Exec(`DROP INDEX IF EXISTS uq_payments_ipay_provider_order;`).Error
		},
	})
}
