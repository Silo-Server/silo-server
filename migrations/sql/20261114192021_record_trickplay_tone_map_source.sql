-- +goose Up
-- The HDR conversion the published sheets were made with (see
-- trickplay.toneMapKey). Reconcile walks finished rows in file order from a
-- persisted cursor, compares it with the conversion the file's current
-- catalog facts call for, and requeues the file when they differ. NULL means
-- unknown: rows published before this column existed, or by a server that
-- does not record it, until reconcile records the conversion their facts
-- call for. after_id = 0 starts a new walk.
ALTER TABLE public.media_file_trickplay
    ADD COLUMN tone_map_source text;

CREATE TABLE IF NOT EXISTS public.trickplay_tone_map_cursor (
    singleton boolean PRIMARY KEY DEFAULT true,
    after_id bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT trickplay_tone_map_cursor_singleton CHECK (singleton),
    CONSTRAINT trickplay_tone_map_cursor_after_id_check CHECK (after_id >= 0)
);

INSERT INTO public.trickplay_tone_map_cursor DEFAULT VALUES
ON CONFLICT (singleton) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS public.trickplay_tone_map_cursor;

ALTER TABLE public.media_file_trickplay
    DROP COLUMN tone_map_source;
