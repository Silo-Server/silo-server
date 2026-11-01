-- +goose Up
-- Kept embedded subtitle tracks are held to a byte budget (see
-- docs/architecture/embedded-subtitle-index.md#kept-tracks). A keep reserves
-- its bytes before storing its object: against its file's share, under a
-- per-file advisory lock, and against the store's total, under this row's
-- lock, evicting the least recently used rows when the total is full.
LOCK TABLE public.embedded_subtitle_tracks IN SHARE ROW EXCLUSIVE MODE;

-- The total of every row's byte_size, kept by the triggers below so a keep
-- reads it without summing the table.
CREATE TABLE public.embedded_subtitle_track_usage (
    id boolean PRIMARY KEY DEFAULT true,
    kept_bytes bigint NOT NULL,
    CONSTRAINT embedded_subtitle_track_usage_one_row CHECK (id)
);

INSERT INTO public.embedded_subtitle_track_usage (kept_bytes)
SELECT COALESCE(SUM(byte_size), 0) FROM public.embedded_subtitle_tracks;

-- Bytes of tracks being stored, which count until their row lands or the
-- lease runs out. Without a foreign key, deleting a file never waits on one.
CREATE TABLE public.embedded_subtitle_track_reservations (
    media_file_id integer NOT NULL,
    embedded_stream_index integer NOT NULL,
    byte_size bigint NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (media_file_id, embedded_stream_index),
    CONSTRAINT embedded_subtitle_track_reservations_bytes_check CHECK (byte_size > 0)
);

-- Eviction walks rows from the least recently used.
CREATE INDEX embedded_subtitle_tracks_recency_idx
    ON public.embedded_subtitle_tracks (updated_at, media_file_id, embedded_stream_index);

-- +goose StatementBegin
CREATE FUNCTION public.count_embedded_subtitle_track_bytes()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    delta bigint;
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        UPDATE public.embedded_subtitle_track_usage SET kept_bytes = 0;
        RETURN NULL;
    ELSIF TG_OP = 'INSERT' THEN
        SELECT COALESCE(SUM(byte_size), 0) INTO delta FROM new_rows;
    ELSIF TG_OP = 'DELETE' THEN
        SELECT -COALESCE(SUM(byte_size), 0) INTO delta FROM old_rows;
    ELSE
        SELECT COALESCE((SELECT SUM(byte_size) FROM new_rows), 0)
            - COALESCE((SELECT SUM(byte_size) FROM old_rows), 0) INTO delta;
    END IF;
    -- Statements that change no bytes (a touch, a delete that matched
    -- nothing) leave the row, and its lock, alone.
    IF delta <> 0 THEN
        UPDATE public.embedded_subtitle_track_usage SET kept_bytes = kept_bytes + delta;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER embedded_subtitle_tracks_count_inserted
    AFTER INSERT ON public.embedded_subtitle_tracks
    REFERENCING NEW TABLE AS new_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.count_embedded_subtitle_track_bytes();

CREATE TRIGGER embedded_subtitle_tracks_count_updated
    AFTER UPDATE ON public.embedded_subtitle_tracks
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.count_embedded_subtitle_track_bytes();

CREATE TRIGGER embedded_subtitle_tracks_count_deleted
    AFTER DELETE ON public.embedded_subtitle_tracks
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.count_embedded_subtitle_track_bytes();

CREATE TRIGGER embedded_subtitle_tracks_count_truncated
    AFTER TRUNCATE ON public.embedded_subtitle_tracks
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.count_embedded_subtitle_track_bytes();

-- +goose Down
DROP TRIGGER IF EXISTS embedded_subtitle_tracks_count_truncated ON public.embedded_subtitle_tracks;
DROP TRIGGER IF EXISTS embedded_subtitle_tracks_count_deleted ON public.embedded_subtitle_tracks;
DROP TRIGGER IF EXISTS embedded_subtitle_tracks_count_updated ON public.embedded_subtitle_tracks;
DROP TRIGGER IF EXISTS embedded_subtitle_tracks_count_inserted ON public.embedded_subtitle_tracks;
DROP FUNCTION IF EXISTS public.count_embedded_subtitle_track_bytes();
DROP INDEX IF EXISTS public.embedded_subtitle_tracks_recency_idx;
DROP TABLE IF EXISTS public.embedded_subtitle_track_reservations;
DROP TABLE IF EXISTS public.embedded_subtitle_track_usage;
