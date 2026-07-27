# Payments

## What's supported

| Method | Where | Status |
|---|---|---|
| Apple In-App Purchase | iOS app | Working (`/api/subscriptions/apple-verify`) |
| Card — Bank of Georgia Payments API | iOS, Android, web | **Implemented, needs credentials** |
| Card — iPay | anywhere | Legacy fallback, deprecated by BOG |

## iPay is deprecated — and it is the same bank

This surprises people, so stating it plainly: **iPay IS Bank of
Georgia.** They are not competitors or separate integrations — they are
successive generations of one product.

- iPay (old): `https://ipay.ge/opay/api/v1/...`
- BOG Payments API (current): `https://api.bog.ge/payments/v1/...`

BOG's own docs mark iPay deprecated:

> "Ipay integration is no longer available. To integrate Bank of Georgia
> payment methods, please use the Payment Manager technical
> documentation."

Existing merchants generally keep working, but the old gateway receives
no new features — notably **no Apple Pay / Google Pay** and no modern
saved-card flows. Migration is a when, not an if.

**Unverified:** whether BOG has announced a shutdown date for existing
iPay merchants. Worth asking your BOG account manager, since it sets the
deadline.

## Switching to BOG

1. Get `client_id` / `client_secret` from https://businessmanager.bog.ge
2. Set in Railway:
   ```
   BOG_CLIENT_ID=...
   BOG_SECRET_KEY=...
   BOG_PUBLIC_KEY=...   # optional but recommended, see below
   ```
3. That's it. `PAYMENT_PROVIDER` can stay unset — the server prefers
   BOG automatically whenever its credentials are present, and falls
   back to iPay when they aren't. Set `PAYMENT_PROVIDER=ipay` to
   force the old gateway, or `=bog` to fail loudly instead of falling
   back.

No mobile release is needed: the app shows one "Pay with Card" button
and the gateway is chosen server-side.

### Callbacks during a switchover

Callbacks dispatch on the provider stored on each subscription row, not
on current config — so orders opened on iPay still settle on iPay after
you flip to BOG. You can switch mid-day without stranding in-flight
payments.

## Security model

Two independent layers; the second is the one that actually gates
activation.

1. **Signature.** BOG signs callbacks with `Callback-Signature`
   (SHA256withRSA). Verified against `BOG_PUBLIC_KEY` over the **raw
   request body** — BOG requires verifying before deserialization
   because JSON field order is part of the signed payload, so
   re-marshalling a decoded struct would invalidate a good signature.
   A present-but-invalid signature is rejected outright.

2. **Re-fetch (authoritative).** The callback body is treated as a
   *hint that something happened*, never as proof. The real status is
   always re-fetched from the gateway with our own OAuth token before
   any subscription is activated. Anyone can POST a callback claiming
   success; order references are guessable.

Because of layer 2, a missing signature or unconfigured public key
degrades safely rather than blocking payments. BOG marks the header
itself as optional. **Do not "simplify" this by trusting the callback
body** — that is the difference between a webhook and an open endpoint
for granting free subscriptions.

Activation is idempotent: the status flip and pet activation run in one
transaction guarded by `WHERE status='pending'`, so redelivered
callbacks can't double-extend an expiry.

`external_order_id` doubles as BOG's `Idempotency-Key` and is minted per
checkout **attempt** from `crypto/rand`. Reusing it would make BOG
return the original order and could strand a customer on a stale
payment page.

## Apple: why iOS still needs IAP

App Store Review Guideline **3.1.1**:

> "If you want to unlock features or functionality within your app, (by
> way of example: **subscriptions**, in-game currencies, game levels,
> access to premium content, or unlocking a full version), you must use
> in-app purchase."

A VetApp subscription unlocks in-app features, so on iOS it must be
purchasable via IAP. Card payment can be offered **alongside** IAP
(which is what the app does), but it cannot replace it — and 3.1.3(e)
(external payment for real-world services) does not cover a
feature-unlocking subscription.

Two things worth knowing:

- **Real-world vet services are different.** Paying an actual clinic for
  an actual visit is a physical service under 3.1.3(e) and can go
  through BOG/TBC on every platform, cleanly. If billing for clinic
  visits is ever added to the app, it does not owe Apple a commission.
- **The US carve-out is real but unstable.** 3.1.1(a) currently exempts
  US-storefront apps from the anti-steering rules. As of 2026-07 the
  injunction is live (Kagan denied Apple's stay, 2026-05-06) but the
  Supreme Court granted cert (2026-06-30), so it may not survive. It
  never applied to the Georgian storefront anyway. Don't build
  monetization that depends on it.

Android and web have no such constraint — BOG/TBC can be the primary
rail there.

## TBC Bank

Not implemented. TBC is a genuinely separate bank, so it needs its own
merchant relationship: NBG license, active business status, VISA
certification, and a TBC current account.

Worth adding for acquiring redundancy or better rates, not for
capability — BOG already covers cards, saved cards and recurring
billing. Their API shape (`api.tbcbank.ge/v1/tpay`) differs from BOG's:
two-tier auth (`apikey` header **plus** a Bearer token) and a
`saveCard` → `recId` → `/payments/execution` recurring flow.

One caution for whoever implements it: TBC's public docs describe
`callbackUrl` but **do not document a callback signing scheme**. Treat
their callbacks as unsigned hints and rely on polling
`/tpay/payments/{payId}` for authoritative status — i.e. exactly the
layer-2 pattern above.

## Apple Pay / Google Pay through the banks

Both banks support wallets, but not as a free pass-through. BOG's Google
Pay flow is **merchant-hosted**: you configure your own Google Pay
environment (gateway `georgiancard`), obtain the encrypted token
client-side, and post it with `"external": true`. Apple Pay follows the
same token pattern. In React Native that means real work — Apple Pay
merchant certificates plus a native payment library.

The cheap path, if wallet buttons are the goal, is BOG's hosted
`payment.bog.ge` page, where they can appear without the app handling
tokens at all. That is what the current WebView checkout already uses.
