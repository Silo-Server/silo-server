-- Dedicated home for Jellyfin DisplayPreferences blobs, which used to ride
-- user_settings under synthetic jellycompat:* keys. They are the jellycompat
-- subsystem's storage rather than user settings — the settings contract
-- neither validates nor resolves them — so rehoming them removes the last
-- non-settings tenant of the legacy key/value table and lets the legacy
-- settings API drop its jellycompat carve-out.
--
-- value is text, not jsonb, deliberately: the blob is opaque Jellyfin client
-- JSON served back byte-for-byte, and jsonb would re-serialize it (reordering
-- keys, normalizing numbers). This table stores what the client sent; it does
-- not interpret it. The per-user SQLite store carries the same table with
-- user_id omitted.
--
-- The data copy out of user_settings is the companion Go migration
-- (jellycompat_displayprefs_move in internal/database): the key parsing is
-- shared with the SQLite backend through internal/jellycompat/displayprefs,
-- which SQL cannot express without duplicating those rules.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.jellycompat_displayprefs (
    user_id    integer NOT NULL,
    prefs_id   text NOT NULL,
    client     text NOT NULL,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT jellycompat_displayprefs_pkey
        PRIMARY KEY (user_id, prefs_id, client),
    CONSTRAINT jellycompat_displayprefs_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.jellycompat_displayprefs;
-- +goose StatementEnd
