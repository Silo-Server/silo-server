# Email invitation API

These v2 operations manage email-bound bearer-token invitations. Ordinary signup
invite codes use the separate auth/signup contract. Administrator operations
require an acting administrator. Public lookup and acceptance use the invitation
rate-limit bucket. Capability responses use `Cache-Control: private, no-cache` and an `ETag`, and support conditional requests. Other responses use `Cache-Control: no-store`, including credentials
and claim links. No raw claim token is retained for response replay.

| Method | Path under `/api/v2` | Behavior |
| --- | --- | --- |
| GET | `/invitations/capabilities` | Public support and profile-store capability |
| GET | `/invitations/{token}` | Claim-screen details for an eligible token |
| POST | `/invitations/{token}/accept` | Atomically create the bound account; report login separately |
| GET | `/admin/invitations/capabilities` | Administrator support and profile-store capability |
| GET | `/admin/invitations` | Bounded administrator history page |
| GET | `/admin/invitations/{id}` | Administrator metadata, without a claim token or digest |
| POST | `/admin/invitations` | Create and attempt delivery of a fresh invitation |
| POST | `/admin/invitations/{id}/resend` | Replace the exact pending/expired source with a new invitation |
| DELETE | `/admin/invitations/{id}` | Idempotently revoke that link; never delete an account |

Capabilities use the shared `revision` and `state` head, plus `default_profile`
and `profileless`. `default_profile` reflects the actual selected provider's
transaction capability, including its notification wrapper. It is false for
SQLite; profileless acceptance remains available. Capability is not a live
storage health check. A claim lookup's `acceptance_available` is false when that
particular invitation requires an unsupported default profile. The route remains
present and a refused operation returns a typed capability problem.

Public lookup returns `email`, `inviter_name`, `server_name`, `expires_at`,
`show_tour`, and `acceptance_available`. Unknown, revoked, expired, and consumed
tokens return the same `404 not_found`. Real storage errors return a safe server
error rather than being disguised as missing tokens.

Acceptance takes `{password}` and returns **201** after the account and invitation
commit. Passwords require at least eight characters and at most 72 UTF-8 bytes.
Its body is `{status: "accepted", login_status, username, tokens?}`:

- `signed_in` includes the ordinary v2 token pair and typed account.
- `sign_in_required` means acceptance committed but session issuance failed.
  Tokens are absent. Offer ordinary sign-in using the selected password; do not
  submit acceptance again or report that account creation failed.

A lost commit response remains uncertain. The invitation token is single-use;
it is not an idempotency key or a way to replay a lost credential response.

Administrator creation takes `email`, optional `role` (default `user`), optional
string `access_group_id`, optional string-ID array `library_ids`, optional
`create_profile` and `show_tour` (both default true), and optional `note`.
Omitted library IDs inherit access; `[]` is an explicit empty override. Omit
optional members instead of sending null. Explicit false must remain false.
Default-profile requests are refused before effects when unsupported.

Create and resend both return **201**, with `Location` pointing to the new
administrator metadata resource. The body contains `invitation`, `claim_url`,
and `delivery_status`:

- `sent`: the configured sender returned success; recipient delivery is not guaranteed.
- `not_configured`: no email was sent; offer manual delivery of the returned link.
- `failed_or_unknown`: the invitation committed but SMTP failed or its result was
  uncertain. The new link is still active and the previous link is revoked.
  Offer the returned link and an explicit administrator decision; do not retry
  the POST automatically.

Claim URLs appear only in creation/replacement responses. Lists and metadata
reads contain neither the raw token, its digest, nor a reusable link. Clients
must keep the disclosed link out of durable query/mutation caches and clear it
when its result UI is dismissed or authority changes.

The administrator list uses `limit` (default 50, maximum 200) and an opaque
`cursor`. Responses contain `items` and required `page` metadata. Ordering is
`created_at DESC, id DESC`, retaining full database timestamp precision in the
cursor. The cursor binds the operation, acting account/profile and page size.
Load continuation explicitly; errors do not mean the retained rows are complete.
IDs are strings; timestamps use UTC millisecond instants. Metadata `library_ids`
is null for inherited access and an array, including an empty array, for an
explicit override.

Resend uses the exact source resource identity as a domain admission guard: an
accepted, revoked, or superseded source returns conflict and cannot revoke a newer
link. Revoke is naturally idempotent against its exact ID and returns **204** with
no body. Neither operation edits invitation access fields in place; there is no
editor ETag protocol for these immutable source choices. Any future configuration
edit must add a transactional configuration precondition.

All three POST operations are **non-retryable**, including authentication-refresh
replay. Revoke is naturally idempotent. See
[the storage invariants](architecture/invitations-onboarding.md) for transaction,
SQLite, expiry, login, and delivery boundaries. Legacy invitation routes remain
available; consumer migration is reviewed separately.


## V2 signup invite codes

Signup invite codes are distinct from emailed invitations. Their administrator
operations are under `/api/v2/admin/invite-codes`:

| Method | Suffix | Result |
|---|---|---|
| GET | `/capabilities` | Availability and client-selected-code support |
| GET | (none) | Cursor-paged code metadata, including use count |
| POST | (none) | Create or resolve an existing code; `201` |
| PUT | `/{id}` | Assign label, maximum uses, or enabled state; `204` |
| POST | `/{id}/top-up` | Add uses once; return current code |
| DELETE | `/{id}` | Delete the code; `204`, or `404` when absent |

All operations require acting-admin authority. Mutations retain the demo guard;
reads do not. IDs and
creator IDs are JSON strings. Lists take `limit` (1–200, default 50) and an opaque
`cursor`, ordered by ID descending. Cursors bind to the administrator account,
profile, and page size.

Creation requires `code` and positive `max_uses`; `label` is optional. The caller
chooses the code before sending. Retrying that code resolves to its existing row
when creator, label, and maximum uses still match; it never replenishes redeemed
uses or enables a disabled code. Changed configuration returns `409`. The web form
generates a code once and retains it after an uncertain response.

Updates accept optional non-null `label`, positive `max_uses`, and `enabled`.
Absolute assignments retain the existing last-writer-wins policy without
`If-Match`. Top-up accepts positive `additional_uses`; it adds to the maximum and
is non-retryable. After an uncertain response, inspect the current maximum before
trying again. The web disables automatic retries and authentication replay for
these mutations and exposes explicit continuation for additional list pages.

The signup redemption contract and frozen v1 administrator routes are unchanged.
There are no Apple, Android, or Jellyfin administrator invite-code consumers to
migrate.
