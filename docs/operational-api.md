# Operational discovery API

`GET /api/v2/compat/connect-info` is an authenticated account read. It requires
no acting profile. `jellyfin` reports `enabled`, `pending_restart`, `public_url`,
and `server_name`. The enabled flag reflects the running listener, while pending
restart compares its state with stored settings. Only the three compatibility
settings needed for this view are read from settings; missing ones fall back to
boot configuration.

`account` says how a password sign-in that names no provider, as a Jellyfin
client's does, goes for the account. The server answers it with the routing and
local-password checks that sign-in runs (`auth.Service.PasswordSignInFor`):

- `password_login_available`: the account's own Silo password signs in. It has
  a password, its own password sign-in is on, `auth.local_password_login` is on
  or the account is break-glass, and no network identity provider refuses the
  person.
- `directory_login_available`: the account's own password does not sign in,
  and it has an identity at the enabled directory (LDAP) provider, with a
  recorded username that a sign-in sends to that directory, so the directory
  username and password sign in. A PIN appended to the password never reaches
  the directory, so PIN-protected profiles cannot be reached this way.
- `directory_username`: that identity's username, present when
  `directory_login_available` is true.

Neither flag is true for a disabled account or one that must change a
temporary password first: compatibility sign-ins refuse both. At most one of
the two flags is true. When the account's sign-in cannot be read,
the answer is `password_login_available: true`. The web Connect Apps page uses
this v2 operation.

`GET /api/v2/images/capabilities` reports capability `revision` and `state`, the
query parameter `param`, ordered `sizes`, per-artwork-type `widths`, and
`original_max_width_px`, plus the members described in
[the images API](images-api.md). Widths come from the current variant ladder. The image
operation accepts account-only requests; when an acting profile is supplied,
viewer-access and PIN verification still apply.

Both are structured Huma reads and return Problem Details for authorization,
validation, or unavailable wiring. Compatibility listener behavior and image delivery URLs do not change.
Apple image discovery, including TopShelf, must use the v2 capability path. No
Android image-discovery or native compatibility-connection consumer was found in
the pinned migration inventory.
