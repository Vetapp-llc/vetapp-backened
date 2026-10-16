package migrations

import "gorm.io/gorm"

// 019 — keep Supabase's public API roles out of every table.
//
// Supabase grants `anon` and `authenticated` full rights on new tables in
// the public schema, and its REST API serves whatever those roles can
// read. Nothing in VetApp uses that API (all access goes through the Go
// backend as the table owner), but the tables created by our migrations
// — otp_codes, email_verification_tokens, idempotency_keys and others —
// were readable and writable with the project's anon key: enough to read
// or plant an SMS code. Revoke those rights, stop granting them on new
// objects, and turn on row-level security on the app's own tables (the
// owner bypasses it). Skipped where the roles don't exist (local
// Postgres, tests).
func init() {
	Register(Migration{
		ID: "019_revoke_public_api_roles",
		Up: func(db *gorm.DB) error {
			return db.Exec(`
			DO $$
			DECLARE r record;
			BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
					RETURN;
				END IF;
				FOR r IN SELECT c.relname, c.relkind FROM pg_class c
						JOIN pg_namespace n ON n.oid = c.relnamespace
						WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
				LOOP
					IF r.relkind = 'S' THEN
						EXECUTE format('REVOKE ALL ON SEQUENCE public.%I FROM anon, authenticated', r.relname);
					ELSE
						EXECUTE format('REVOKE ALL ON TABLE public.%I FROM anon, authenticated', r.relname);
					END IF;
				END LOOP;
				ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM anon, authenticated;
				ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM anon, authenticated;
				ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON FUNCTIONS FROM anon, authenticated;
				FOR r IN SELECT c.relname FROM pg_class c
						JOIN pg_namespace n ON n.oid = c.relnamespace
						WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname IN (
							'otp_codes', 'email_verification_tokens', 'pet_share_tokens', 'idempotency_keys',
							'app_changes', 'sms_runs', 'procedure_files', 'schema_migrations')
				LOOP
					EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', r.relname);
				END LOOP;
			END $$;
			`).Error
		},
	})
}
