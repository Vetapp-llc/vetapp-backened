# VetApp Backend

Backend API for a veterinary clinic management system. Handles pet records, medical procedures, appointments, payments, staff management, and a pet owner mobile portal with subscription-based access.

## Tech Stack

- **Go** — Chi router, GORM ORM
- **PostgreSQL** — Supabase hosted
- **JWT** — Access + refresh token auth
- **Swagger** — Auto-generated OpenAPI docs at `/swagger/`
- **smsoffice.ge** — SMS gateway (OTP, reminders)
- **iPay.ge** — Payment gateway (subscriptions)

## Setup

```bash
cp .env.example .env  # fill in values
make dev              # generates swagger + starts server
```

## Type Safety

Swagger annotations on all handlers auto-generate `swagger.json`. The frontend runs `npm run generate:types` to produce matching TypeScript types.
