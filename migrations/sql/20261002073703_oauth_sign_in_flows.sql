-- +goose Up
-- +goose StatementBegin
-- OAuth sign-in flows (docs/architecture/external-sign-in.md, "OAuth flows").
--
-- oauth_sessions: binder_hash is the SHA-256 of the browser-binding cookie
-- the flow's start set; the callback accepts only the browser that holds it,
-- and a flow row without one (started before this migration) is refused
-- ("Browser binding"). A native flow (the phone and desktop apps) carries
-- the app's PKCE S256 code_challenge and its opaque app_state, and its
-- callback redirects to the fixed app URI instead of the web completion
-- page. start_origin is the origin where an app's native flow first arrived
-- (scheme://host[:port], the app's saved server address); every app
-- redirect of the flow names it as iss. Web flows leave it empty
-- ("Native apps").
ALTER TABLE oauth_sessions
    ADD COLUMN binder_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN flow_kind TEXT NOT NULL DEFAULT 'web',
    ADD COLUMN code_challenge TEXT NOT NULL DEFAULT '',
    ADD COLUMN app_state TEXT NOT NULL DEFAULT '',
    ADD COLUMN start_origin TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT oauth_sessions_flow_kind CHECK (flow_kind IN ('web', 'native'));

-- oauth_completions ("Completion codes"): a completion code opens its login
-- session when it is redeemed, not at the callback, so a code nobody redeems
-- leaves no session behind. The row keeps what the session needs: the
-- account, the identity the sign-in came through (no foreign key: the
-- session insert refuses an identity removed in between), and the device
-- name and client address of the browser the callback answered.
-- token_ciphertext stays empty for these rows. The row outlives its
-- redemption: session_id and redeemed_at are set then, so a second
-- redemption of the same code is recognized and revokes that session.
-- browser_hash is the SHA-256 (hex) of the random value of the completion
-- cookie the callback set; a web code redeems only with that cookie. Native
-- codes, bound to their code_challenge instead, leave it empty. Rows
-- written before this migration name no account and no browser, so none of
-- them redeems.
ALTER TABLE oauth_completions
    ADD COLUMN flow_kind TEXT NOT NULL DEFAULT 'web',
    ADD COLUMN code_challenge TEXT NOT NULL DEFAULT '',
    ADD COLUMN user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    ADD COLUMN identity_id BIGINT,
    ADD COLUMN device_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN ip_address TEXT NOT NULL DEFAULT '',
    ADD COLUMN browser_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN session_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN redeemed_at TIMESTAMPTZ,
    ALTER COLUMN token_ciphertext SET DEFAULT '',
    ADD CONSTRAINT oauth_completions_flow_kind CHECK (flow_kind IN ('web', 'native'));

-- A link ticket lets a signed-in account, after re-entering its password,
-- start a linking flow in a browser that holds no bearer token. Single use,
-- short-lived, bound to one auth plugin installation ("Linking").
CREATE TABLE oauth_link_tickets (
    ticket_hash     TEXT PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    installation_id INTEGER NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX oauth_link_tickets_expires_idx ON oauth_link_tickets (expires_at);

-- A native app's linking flow does not link at the provider callback: the
-- callback parks the provider's answer here under a one-time code bound to
-- the app's PKCE S256 challenge, and the link is made only when the app
-- redeems the code with its verifier and the bearer token of the account
-- that asked for the link ticket. Only the code's SHA-256 is stored; the
-- answer (which may carry the provider's refresh state) is AES-GCM
-- encrypted under a key derived from the JWT secret and bound to the code
-- hash.
CREATE TABLE oauth_pending_links (
    code_hash       TEXT PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    installation_id INTEGER NOT NULL,
    capability_id   TEXT NOT NULL,
    code_challenge  TEXT NOT NULL,
    payload         TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX oauth_pending_links_expires_idx ON oauth_pending_links (expires_at);

-- A native start that arrives on another origin than the public URL waits
-- here while the browser moves to the public origin, carrying only the
-- random flow id (only its hash is stored). The public-origin start deletes
-- the row and opens the flow from it, so nothing that names the start origin,
-- the challenge or the app's state travels in the browser. Single use, a
-- couple of minutes.
CREATE TABLE oauth_native_starts (
    flow_hash       TEXT PRIMARY KEY,
    installation_id INTEGER NOT NULL,
    code_challenge  TEXT NOT NULL,
    app_state       TEXT NOT NULL,
    prompt          TEXT NOT NULL DEFAULT '',
    linking_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    start_origin    TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX oauth_native_starts_expires_idx ON oauth_native_starts (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS oauth_native_starts;
DROP TABLE IF EXISTS oauth_pending_links;
DROP TABLE IF EXISTS oauth_link_tickets;

ALTER TABLE oauth_completions
    DROP CONSTRAINT IF EXISTS oauth_completions_flow_kind,
    ALTER COLUMN token_ciphertext DROP DEFAULT,
    DROP COLUMN IF EXISTS redeemed_at,
    DROP COLUMN IF EXISTS session_id,
    DROP COLUMN IF EXISTS browser_hash,
    DROP COLUMN IF EXISTS ip_address,
    DROP COLUMN IF EXISTS device_name,
    DROP COLUMN IF EXISTS identity_id,
    DROP COLUMN IF EXISTS user_id,
    DROP COLUMN IF EXISTS code_challenge,
    DROP COLUMN IF EXISTS flow_kind;

ALTER TABLE oauth_sessions
    DROP CONSTRAINT IF EXISTS oauth_sessions_flow_kind,
    DROP COLUMN IF EXISTS start_origin,
    DROP COLUMN IF EXISTS app_state,
    DROP COLUMN IF EXISTS code_challenge,
    DROP COLUMN IF EXISTS flow_kind,
    DROP COLUMN IF EXISTS binder_hash;
-- +goose StatementEnd
