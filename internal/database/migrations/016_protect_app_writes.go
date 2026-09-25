package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// 016 — keep the MySQL → Supabase sync from overwriting work done in the
// new app while both systems are live.
//
// The sync (cmd/sync --update) upserts every MySQL row, so before this:
//   - an edit made in the new app to a legacy row was reverted by the
//     next run,
//   - a row deleted in the new app was re-inserted as "missing",
//   - a row created in the new app took the next id after MySQL's
//     highest, and was overwritten once PHP issued that id.
//
// Two mechanisms fix that:
//
//  1. Id ranges. Rows the new app creates get ids from AppIDBase up;
//     MySQL is around 160k and never reaches it, so the two sources can
//     no longer claim the same id.
//
//  2. A change log. A trigger on every legacy table the app writes
//     records each app-side edit or delete of a MySQL-origin row in
//     `app_changes`. The sync skips those rows (the app's version wins)
//     and re-applies the deletes. The sync marks its own writes with
//     SET LOCAL vetapp.sync = 'on', which the trigger ignores.
//
// Updates that touch only app-owned bookkeeping columns (a login
// stamping last_login, a bcrypt backfill, a push token) are not edits of
// the legacy record and are not logged — otherwise every sign-in would
// freeze that account against later changes made in PHP.

// AppIDBase is the first id the new app assigns in a synced table.
// Tables use int4 ids, so this leaves ~1.1 billion ids for the app.
const AppIDBase = 1_000_000_000

// protectedTables are the legacy tables the new app writes to.
var protectedTables = []string{
	"vaccination", "pets", "memberlogin_members", "paymethod", "shop", "prices",
	"eals", "alergy", "operationdate", "homepro", "pro", "analysefile", "payments_ipay",
}

func init() {
	Register(Migration{
		ID: "016_protect_app_writes",
		Up: func(db *gorm.DB) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS app_changes (
					table_name TEXT NOT NULL,
					row_id     BIGINT NOT NULL,
					op         TEXT NOT NULL,
					changed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					PRIMARY KEY (table_name, row_id)
				)`,
				fmt.Sprintf(`CREATE OR REPLACE FUNCTION track_app_change() RETURNS trigger AS $$
				DECLARE
					rid BIGINT;
					bookkeeping TEXT[] := ARRAY['password_hash', 'last_login', 'refreshToken', 'fcm_token', 'emailVerified', 'phoneVerified'];
				BEGIN
					IF current_setting('vetapp.sync', true) = 'on' THEN
						RETURN NULL;
					END IF;
					IF TG_OP = 'DELETE' THEN rid := OLD.id; ELSE rid := NEW.id; END IF;
					IF rid >= %d THEN
						RETURN NULL; -- the app's own range; MySQL never has these ids
					END IF;
					IF TG_OP = 'UPDATE' AND (to_jsonb(NEW) - bookkeeping) = (to_jsonb(OLD) - bookkeeping) THEN
						RETURN NULL;
					END IF;
					INSERT INTO app_changes (table_name, row_id, op) VALUES (TG_TABLE_NAME, rid, lower(TG_OP))
					ON CONFLICT (table_name, row_id) DO UPDATE SET op = EXCLUDED.op, changed_at = NOW();
					RETURN NULL;
				END
				$$ LANGUAGE plpgsql`, AppIDBase),
			}
			for _, s := range stmts {
				if err := db.Exec(s).Error; err != nil {
					return err
				}
			}
			for _, t := range protectedTables {
				var hasID bool
				db.Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
					WHERE table_schema = 'public' AND table_name = ? AND column_name = 'id')`, t).Scan(&hasID)
				if !hasID {
					continue
				}
				if err := db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS track_app_change ON %q;
					CREATE TRIGGER track_app_change AFTER INSERT OR UPDATE OR DELETE ON %q
					FOR EACH ROW EXECUTE FUNCTION track_app_change()`, t, t)).Error; err != nil {
					return err
				}
				// Move the id sequence into the app range (never backwards).
				var seq string
				db.Raw(`SELECT COALESCE(pg_get_serial_sequence(?, 'id'), '')`, "public."+t).Scan(&seq)
				if seq == "" {
					continue
				}
				if err := db.Exec(fmt.Sprintf(`SELECT setval('%s', GREATEST((SELECT last_value FROM %s), %d))`,
					seq, seq, AppIDBase)).Error; err != nil {
					return err
				}
			}
			return nil
		},
	})
}
