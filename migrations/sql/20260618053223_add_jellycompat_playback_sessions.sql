-- +goose Up
-- +goose StatementBegin
-- jellycompat_playback_sessions durably stores the compat (Jellyfin) playback
-- negotiation session so a Jellyfin client can resume across a server restart.
-- It holds the load-bearing PlaySessionId -> upstream-session mapping plus the
-- negotiated media sources, route item id, and seek (the full PlaybackSession in
-- the data JSONB). Without it, after a restart the in-memory map is empty and the
-- compat segment/manifest handlers 404 at their first lookup before any transcode
-- reconstruct can run. One row per active compat play session; rows expire via
-- expires_at (the negotiation TTL, re-armed on update) and are swept by the
-- reconciler janitor. compat_token is denormalized for lookup; user_id is
-- denormalized for auditing. The canonical state is the data column.
CREATE TABLE IF NOT EXISTS public.jellycompat_playback_sessions (
    id            TEXT PRIMARY KEY,
    compat_token  TEXT NOT NULL DEFAULT '',
    user_id       TEXT NOT NULL DEFAULT '',
    data          JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL
);

-- FindByRoute scans live (non-expired) rows for a matching compat token; the
-- expiry sweep filters on expires_at. Index both access paths.
CREATE INDEX IF NOT EXISTS idx_jellycompat_playback_sessions_expires_at
ON public.jellycompat_playback_sessions USING btree (expires_at);

CREATE INDEX IF NOT EXISTS idx_jellycompat_playback_sessions_compat_token
ON public.jellycompat_playback_sessions USING btree (compat_token);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.jellycompat_playback_sessions;
-- +goose StatementEnd
