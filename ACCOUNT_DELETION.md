# Account deletion while the old website remains primary

The approved service window is **7 days**. The old PHP/MySQL website is
still primary; deletion must cover it and the Go/Postgres app system.
One-way MySQL-to-Postgres sync does not propagate deletion to MySQL.

## Receive requests

Owners open **Other → Delete account**, confirm the current password,
then submit inside the app. They see a durable request number and due
date. Repeating the request does not create another ticket or extend
the deadline. They retain account access until staff complete erasure.
No password is stored in the request queue.

Staff must check the private **Account deletion** tab at
`https://vetapp-web.vercel.app/en/admin` daily. It is available only to
existing super-admin accounts. Pending requests also appear as a notice
on the admin overview. The request contains the account email and
personal ID needed to locate the corresponding old account.

## Fulfill a request

1. Match the account in **both** systems using the request's account ID,
   email and personal ID. Check for shared identifiers or another
   owner's records before erasing anything. Do not erase clinic staff
   accounts or another owner's data.
2. Remove the old account and its associated owner-created data and
   uploads in the legacy system. The old admin's member-row deletion
   alone is insufficient. Remove legacy owner profiles, home treatments,
   owner-added medical content, personal attachments and sharing access.
   Retain only clinic/financial records required by applicable retention
   obligations; remove unnecessary account/contact links from those.
3. Remove the new account and associated owner data, private uploaded
   files and sharing tokens in Postgres/Storage. Check copied legacy
   tables too. Preserve sync tombstones in `app_changes` so a later sync
   cannot resurrect erased Postgres rows. Migration 021's file-removal
   queue must be drained or successfully retried for relevant objects.
4. Revoke cached PHP sessions for this member on the old host and all
   app sessions. The old PHP authorization checks a cached session;
   deleting its member row alone does not reliably revoke that session.
5. Verify the affected account cannot sign in in either system, check
   that private files/sharing links no longer resolve, and confirm that
   unrelated clinic/owner accounts still work.
6. Send completion confirmation to the request's account email, including
   any required retained-record explanation. Do not include passwords,
   personal ID numbers or medical content in that email.
7. Tick the four completion checks in the private queue and record
   completion. The API refuses closure while a matching owner account
   still exists in Postgres. Staff explicitly attest to legacy/data
   erasure, session revocation and owner notification. Completion clears
   the queue's copied contact/identity data and owner ID; the request
   number, timestamps and completing administrator remain as an audit.

These are **manual operational steps**; the queue does not automatically
erase either system or send email. Do not mark a request complete after
merely disabling a login. Handle unexpected shared identities and legally
required retention individually within the stated deadline.

## Release checks

- Keep `OWNER_ACCOUNT_DELETION_ENABLED` off: the separate immediate
  erasure endpoint covers Postgres only and is not a cross-system tool.
- Private schema `vetapp_private` is outside exposed API schemas; public
  grants are revoked and RLS is enabled on request/file queues.
- Local disposable-database tests cover owner/password/confirmation
  checks, role isolation, receipt/deadline idempotency, account-present
  completion refusal and contact-data scrubbing after completion.
- Never exercise erasure or create a real deletion request for the
  persistent App Review demo account during release verification.
