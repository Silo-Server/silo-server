-- +goose Up
-- +goose StatementBegin
-- Recently-Added notifications for audiobooks (issue #270): availability
-- facts for audiobook items, mirroring movie_availability. Rows are written
-- by the availability detector after audiobook library scans; release events
-- are emitted only once the library's back catalog has been seeded (kind
-- 'audiobook' in notification_content_seed_state).
CREATE TABLE audiobook_availability (
    library_id   integer NOT NULL,
    item_id      text COLLATE "C" NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (library_id, item_id)
);

-- Per-channel opt-in toggle. Defaults false everywhere (existing and new
-- channels): operators enable audiobook announcements explicitly, matching
-- the conservative unknown-kind posture in WantsContentKind.
ALTER TABLE notification_server_channels
ADD COLUMN notify_new_audiobooks boolean NOT NULL DEFAULT false;

-- Admit the new kind on release_events.
ALTER TABLE release_events
DROP CONSTRAINT IF EXISTS release_events_kind_check;
ALTER TABLE release_events
ADD CONSTRAINT release_events_kind_check
CHECK (kind = ANY (ARRAY['episode'::text, 'movie'::text, 'audiobook'::text]));

-- Audiobook events are item-shaped, like movies.
ALTER TABLE release_events
DROP CONSTRAINT IF EXISTS release_events_kind_shape_check;
ALTER TABLE release_events
ADD CONSTRAINT release_events_kind_shape_check
CHECK (
    (kind = 'episode' AND series_id IS NOT NULL AND episode_id IS NOT NULL
        AND season_number IS NOT NULL AND episode_number IS NOT NULL AND episode_key IS NOT NULL)
    OR (kind = 'movie' AND item_id IS NOT NULL)
    OR (kind = 'audiobook' AND item_id IS NOT NULL)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE notification_server_channels DROP COLUMN IF EXISTS notify_new_audiobooks;
DROP TABLE IF EXISTS audiobook_availability;
-- +goose StatementEnd
