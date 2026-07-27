# Running VetApp locally

Three pieces: the Go backend, the Next.js clinic portal, and the Expo
mobile app. The backend must be running before the other two are useful.

Your Mac's LAN IP (needed for testing on a physical phone) — re-check it
after switching networks:

```bash
ipconfig getifaddr en0
```

---

## 1. Backend (Go API) — start this first

```bash
cd ~/dev/vetapp/vetapp-backend
go run ./cmd/server
```

Serves on **http://localhost:8080**. Check it with:

```bash
curl http://localhost:8080/health          # {"status":"ok"}
open http://localhost:8080/swagger/        # API docs
```

Config comes from `.env` (already set up). Needs the Supabase database
to be awake — if it errors with `tenant/user ... not found`, the free
tier has auto-paused the project; resume it at
https://supabase.com/dashboard.

**You usually don't need to run this at all** — production is live at
https://vetapp-backened-production-00e8.up.railway.app and both
frontends can point at it. Run it locally only when changing backend code.

---

## 2. Website (Next.js clinic portal)

```bash
cd ~/dev/vetapp/vetapp-web
npm install        # first time only
npm run dev
```

Opens on **http://localhost:3002**. The port is pinned in
`package.json` rather than left to Next.js' auto-increment, because port
3000 belongs to another project on this machine and a moving URL also
means CORS breaks intermittently.

Which backend it talks to is set in `.env.local`:

```
NEXT_PUBLIC_API_URL=http://localhost:8080                              # local
NEXT_PUBLIC_API_URL=https://vetapp-backened-production-00e8.up.railway.app  # production
```

Restart `npm run dev` after changing that file.

This is the **clinic/vet portal**, not the owner app — log in with vet
credentials, not the pet-owner test account.

---

## 3. Mobile app (Expo)

### iOS Simulator

```bash
cd ~/dev/vetapp/vetapp-mobile
npx expo run:ios
```

First run builds the native project (a few minutes). Afterwards
`npx expo start` and pressing `i` is enough.

The app defaults to `http://localhost:8080` in dev, which is correct on
the simulator because it shares the Mac's network.

### Physical iPhone

`localhost` on a phone means the phone itself, so the API URL must be
your Mac's LAN IP:

```bash
cd ~/dev/vetapp/vetapp-mobile
EXPO_PUBLIC_API_URL=http://192.168.100.6:8080 npx expo start
```

Scan the QR code with the Camera app. The phone and Mac must be on the
same Wi-Fi.

Or skip the local backend entirely and use production:

```bash
EXPO_PUBLIC_API_URL=https://vetapp-backened-production-00e8.up.railway.app npx expo start
```

**Expo Go is not enough.** The app uses native modules
(`react-native-iap`, biometrics), so it needs a development build:
`npx expo run:ios` once, then `npx expo start` on subsequent runs.

### Android emulator

```bash
npx expo run:android
```

Android's emulator reaches the host at `10.0.2.2`, which the app already
handles automatically — no env var needed.

---

## Test credentials

**Pet owner (mobile app)** — `vetapp-mobile/creds.txt`

```
test@vetapp.ge / TestApp123
```

5 pets: ქოფი, ფისო, ჩომბე active; რექსი, მიმი unregistered (so both
subscription states are testable). Records include owner-reported and
clinic-created procedures, plus 3 booked visits.

**Clinic / vet (website)**

```
m.chkhikvishvili@yahoo.com / 555275507
```

⚠️ These accounts live in Supabase and can be destroyed by a sync — see
`vetapp-backend/SYNC.md`.

---

## Troubleshooting

**"Network request failed" in the app** — the backend isn't running, or
the phone can't reach it. On a physical device confirm you passed
`EXPO_PUBLIC_API_URL` with the LAN IP. The app logs its resolved URL at
startup: `[api] BASE_URL = ...`.

**"Session expired"** — the token refresh failed. Usually the backend is
down, or the account no longer exists (a sync can overwrite accounts
created directly in Supabase).

**Backend won't start, database error** — Supabase has auto-paused;
resume it from the dashboard.

**Website CORS errors in the browser console** — the backend only
allows a fixed list of origins (3000/3001/3002 by default, see
`allowedOrigins` in internal/router/router.go). If you run the site on
another port, add it to `CORS_ORIGINS` or the browser will block every
API call.

**Don't `pkill -f "next dev"`** — it matches every Next.js dev server on
the machine, including unrelated projects. Kill by port instead:
`lsof -ti:3002 | xargs kill`.

**Card payment fails** — expected. iPay is Bank of Georgia's deprecated
gateway and returns HTML instead of JSON. The replacement (BOG Payments
API) is implemented but needs credentials — see
`vetapp-backend/PAYMENTS.md`.
