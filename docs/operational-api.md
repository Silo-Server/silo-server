# Operational discovery API

`GET /api/v2/compat/connect-info` is an authenticated account read. It requires
no acting profile. `jellyfin` reports `enabled`, `pending_restart`, `public_url`,
and `server_name`; `account.password_login_available` reports whether the account
can use a local password to sign into a compatible client. The enabled flag
reflects the running listener, while pending restart compares its state with
stored settings. Only the three compatibility settings needed for this view are
read. Missing settings fall back to boot configuration, and an unresolved account
retains the bridge's password-availability fallback. The web Connect Apps page
uses this v2 operation.

`GET /api/v2/images/capabilities` reports capability `revision` and `state`, the
query parameter `param`, ordered `sizes`, per-artwork-type `widths`, and
`original_max_width_px`. Widths come from the current variant ladder. The image
operation accepts account-only requests; when an acting profile is supplied,
viewer-access and PIN verification still apply. This preserves the bridge gate.
The v2 response uses capability revision/state instead of legacy `schema_version`.

Both are structured Huma reads and return Problem Details for authorization,
validation, or unavailable wiring. The frozen v1 reads call the same application
views. Compatibility listener behavior and image delivery URLs do not change.
Apple image discovery, including TopShelf, must adopt the v2 capability path and
shape before cutover. No Android image-discovery or native compatibility-connection
consumer was found in the pinned migration inventory. Independent review and
consumer acceptance are tracked in the migration ledger; these operations do not
complete worker protocol negotiation or the 1.0 retirement gates.
