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
| `--full` | TRUNCATE + everything (implies `--update`); refuses if new-app data exists | rebuilding from scratch |
| `--relocate-app-rows` | moves pre-016 app rows into the app id range | once, after deploying migration 016 |
| `--dry-run` | nothing; prints the diff | always, before a real run |

## Work done in the new app is kept (migration 016)

MySQL stays the source of truth for rows that come from it, but the sync no
longer destroys what the new app does:

| New app does | Before | Now |
|---|---|---|
| Edits a row that came from MySQL | reverted by the next `--update` | kept; the sync skips that row |
| Deletes a row that came from MySQL | re-inserted as "missing" | stays deleted |
| Creates a row | took the next id after MySQL's highest, so it was overwritten once PHP issued that id | gets an id from 1,000,000,000 up, which MySQL never reaches |

How: a trigger on every legacy table the app writes to records each app-side
edit or delete in `app_changes`; the sync skips those rows and re-applies the
deletes. The sync marks its own writes (`SET LOCAL vetapp.sync = 'on'`) so they
are not recorded. Updates that only touch login bookkeeping (`last_login`,
`password_hash`, push/refresh tokens, verification flags) are not edits.

App writes and sync writes on the same table take turns (migration 017): each
app statement takes a shared per-table lock before touching rows, each sync batch
takes it exclusively. Without this, an app edit committing while a sync statement
waited on that row was overwritten anyway (reproduced before the fix). App writes
wait at most one sync batch, a few milliseconds.

Every run ends with a "Kept the new app's version" section listing how many
MySQL rows were skipped per table.

**What this does not do.** Sync is still one way. PHP users do not see
anything done in the new app, and when the same record is changed in both
systems the new app's version wins (PHP's change is skipped and counted in the
report). Clinics should not work on the same pet in both systems during the
overlap.

### Rows created before migration 016

Rows the app created earlier sit just above MySQL's highest id and will still
be overwritten when PHP reaches them. Every run lists them
(`WARN … created by the app before migration 016`). Move them into the app range:

```bash
go run ./cmd/sync --update --relocate-app-rows
```

References to a moved row are updated in the same transaction (a pet's
procedures, appointments, allergies, home procedures, payments and files; a
member's records and uploads). A moved **pet gets a new id**, so any QR tag
already printed for it must be reprinted.

Run it **once, right after deploying migrations 016/017**. Rows are detected by
being above MySQL's current highest id; an app row whose id MySQL has *already*
issued cannot be told apart from the MySQL row and has already been (or will be)
overwritten. Those need manual reconciliation: compare `--dry-run`'s per-table
"PG has N extra" counts with the rows the app is known to have created.

### `--full` refuses to erase app data

`--full` truncates every table. It now aborts if any new-app data exists; pass
`--discard-app-data` only when losing it is intended.

**At cutover, run a final `--update`, not `--full`,** then stop writing to MySQL.

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
