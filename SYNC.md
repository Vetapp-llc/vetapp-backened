# Sync: MySQL (live PHP app) → Supabase (new stack)

The PHP app and its MySQL database are **production**. The Go backend,
Next.js portal and mobile app are the replacement, not yet live.
`cmd/sync` keeps Supabase current until the old stack retires.

## Run it

```bash
go run ./cmd/sync --update --dry-run   # see what would change
go run ./cmd/sync --update             # the routine sync
```

**Use `--update` while the PHP app is still live.** Plain incremental
mode compares ids only, so a row edited upstream after it first synced
is never refreshed. Measured on 2026-07-27, before `--update` existed:
592 of 31,958 pets (1.9%) had drifted, including **388 microchip
numbers** present in MySQL but blank in Supabase, because the chip was
registered after the pet first synced. A microchip is a pet's legal
identifier.

| Mode | Copies | Use when |
|---|---|---|
| (default) | rows whose id is missing from PG | append-only tables |
| `--update` | missing rows **and** rows edited upstream | **routine, while PHP is live** |
| `--full` | TRUNCATE + everything (implies `--update`) | rebuilding from scratch |
| `--dry-run` | nothing; prints the diff | always, before a real run |

## ⚠️ Data written directly to Supabase can be destroyed

MySQL is the source of truth and Supabase is a mirror, so `--update`
overwrites any Supabase row whose id also exists upstream.

This is not hypothetical. `test@vetapp.ge` was created directly in
Supabase and landed at `memberlogin_members.id = 843`. MySQL later
issued 843 to a real customer. The next `--update` sync replaced the
test account with that customer's row, and the login started failing
with "session expired".

**Until the PHP app retires:**

- Treat Supabase as read-mostly. Anything created there — accounts,
  pets, procedures — is at risk on the next sync if its id collides.
- Create test accounts knowing they may vanish; note the id.
- Real customer signups should go through the PHP app, or the two
  systems will fight over the same id space.
- Sequences are resynced after each run (`resyncSequences`), which
  stops *new* Supabase rows colliding with *already-imported* ids —
  but it cannot prevent MySQL later issuing an id Supabase already
  used. Only retiring the old app removes that risk.

At cutover, do a final `--full` sync, then stop writing to MySQL.

## What the tool handles

- **Passwords** are re-encrypted from the legacy MySQL AES salt to the
  Supabase one during the sync. The backend's `AES_SALT` must match the
  Supabase salt or no migrated customer can log in — both values are in
  the deployment environment, not in this repo.
- **Binary columns** (AES ciphertext in `*.password`) are passed
  through as bytes. Forcing them through a Go string produced invalid
  UTF-8 that Postgres rejected (SQLSTATE 22021) — it silently failed
  the admin row on every run for weeks.
- **Composite primary keys.** The upsert conflict target is read from
  the Postgres catalog per table. `memberlogin_options` is keyed on
  `(foreign_id, key)`; assuming `id` failed all 83 of its rows.
- **Identity sequences** are fast-forwarded after every run. An
  explicit-id INSERT never advances a Postgres sequence, so without
  this the next natural INSERT collides — this broke pet creation and
  procedure recording entirely in July 2026.
- **Row-level errors are logged**, not just counted. The previous
  behaviour hid real failures behind a bare error count.

## Expected post-sync warnings

```
WARN prices       MySQL=115     PG=132     (diff=-17)
WARN vaccination  MySQL=146293  PG=146294  (diff=-1)
```

Postgres having **more** rows than MySQL is normal — those are records
created by the new backend during development. Postgres having
**fewer** is a real problem: it means rows failed to copy.

A healthy run reports `missing-from-PG = 0` for every table.
