package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// 017 — make app writes and sync writes on the same table take turns.
//
// Migration 016 records app edits in app_changes, and the sync's upsert
// skips rows listed there. Without ordering, an app edit committing while
// a sync statement waits on that row's lock was invisible to the
// statement's (earlier) snapshot of app_changes, so the edit was
// overwritten anyway.
//
// Now every app statement on a protected table first takes a SHARED
// transaction-level advisory lock for the table (a BEFORE STATEMENT
// trigger, i.e. before any row lock), and each sync batch takes the same
// lock EXCLUSIVELY before its write. A sync batch therefore starts only
// after every in-flight app transaction on the table has committed — its
// statement snapshot includes their app_changes rows — and app writes
// wait for the batch (a few ms). Taking the advisory lock before row
// locks on both sides rules out deadlock.
//
// The key is shared with cmd/sync (syncLockKey there).
func init() {
	Register(Migration{
		ID: "017_sync_write_lock",
		Up: func(db *gorm.DB) error {
			if err := db.Exec(`CREATE OR REPLACE FUNCTION app_write_lock() RETURNS trigger AS $$
			BEGIN
				IF current_setting('vetapp.sync', true) = 'on' THEN
					RETURN NULL;
				END IF;
				PERFORM pg_advisory_xact_lock_shared(hashtext('vetapp-sync|' || TG_TABLE_NAME));
				RETURN NULL;
			END
			$$ LANGUAGE plpgsql`).Error; err != nil {
				return err
			}
			for _, t := range protectedTables {
				var exists bool
				db.Raw(`SELECT to_regclass(?) IS NOT NULL`, "public."+t).Scan(&exists)
				if !exists {
					continue
				}
				if err := db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS app_write_lock ON %q;
					CREATE TRIGGER app_write_lock BEFORE INSERT OR UPDATE OR DELETE ON %q
					FOR EACH STATEMENT EXECUTE FUNCTION app_write_lock()`, t, t)).Error; err != nil {
					return err
				}
			}
			return nil
		},
	})
}
