-- +goose Up
-- +goose StatementBegin
-- transcode_recipes stores the small, durable "recipe card" needed to
-- reconstruct an in-progress HLS transcode after the server loses its in-memory
-- state (restart, crash, or an idle/paused reap). One row per active transcode
-- session, written on start and deleted on a genuine user stop. Liveness reaps
-- keep the row so a resume can rebuild under the same session id; abandoned rows
-- expire via expires_at (idle window, re-armed on activity) and are swept by the
-- reconciler janitor. The full encode parameter set lives in the opts JSONB so a
-- reconstructed ffmpeg emits manifest-compatible segments.
CREATE TABLE IF NOT EXISTS public.transcode_recipes (
    session_id          TEXT PRIMARY KEY,
    user_id             INTEGER NOT NULL,
    profile_id          TEXT NOT NULL DEFAULT '',
    media_file_id       INTEGER NOT NULL,
    transcode_node_url  TEXT NOT NULL DEFAULT '',
    opts                JSONB NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL
);

-- Sweep query filters on expires_at; index it so the periodic
-- DELETE ... WHERE expires_at < now() is an index scan.
CREATE INDEX IF NOT EXISTS idx_transcode_recipes_expires_at
ON public.transcode_recipes USING btree (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.transcode_recipes;
-- +goose StatementEnd
