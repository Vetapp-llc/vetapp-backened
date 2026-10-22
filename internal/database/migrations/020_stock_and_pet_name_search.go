package migrations

import "gorm.io/gorm"

// 020 — clinic stock, and pet-name search.
//
// shop_products is a clinic's stock: a product, the units on hand and the
// price of one unit. Selling ("რეალიზაცია") writes an ordinary shop row,
// so revenue reports keep working, and records which product and how
// many units it took in shop_sale_items, so deleting the sale can put
// them back. Both tables are the app's own: the MySQL sync never touches
// them, and shop rows from the old site simply have no item.
//
// Clinic search now matches pet names, which needs the same trigram
// index the owner columns have (008).
func init() {
	Register(Migration{
		ID: "020_stock_and_pet_name_search",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
				CREATE TABLE IF NOT EXISTS shop_products (
					id         BIGSERIAL PRIMARY KEY,
					zip        TEXT NOT NULL,
					name       TEXT NOT NULL,
					quantity   INTEGER NOT NULL DEFAULT 0 CHECK (quantity >= 0),
					unit_price NUMERIC(12, 2) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
					created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
				);
				CREATE INDEX IF NOT EXISTS idx_shop_products_zip_name ON shop_products (zip, name);

				CREATE TABLE IF NOT EXISTS shop_sale_items (
					shop_id    INTEGER PRIMARY KEY,
					product_id BIGINT NOT NULL REFERENCES shop_products (id) ON DELETE CASCADE,
					quantity   INTEGER NOT NULL CHECK (quantity > 0)
				);
				CREATE INDEX IF NOT EXISTS idx_shop_sale_items_product ON shop_sale_items (product_id);

				ALTER TABLE shop_products ENABLE ROW LEVEL SECURITY;
				ALTER TABLE shop_sale_items ENABLE ROW LEVEL SECURITY;

				CREATE INDEX IF NOT EXISTS idx_pets_name_trgm ON pets USING gin (name gin_trgm_ops);
			`).Error
		},
	})
}
