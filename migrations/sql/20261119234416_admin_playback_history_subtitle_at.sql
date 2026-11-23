-- subtitle_at is when the history row's subtitle was last known to be
-- showing: the end instant of the finalization that recorded it, or, for a
-- lifecycle sweep that only had the play's live or start row, when that row
-- was synced. A finalization that gets no further leaves ended_at alone, so
-- the subtitle needs its own ordering, as end_reason has with end_reason_at.
-- NULL on rows written before this column; the next finalization orders it.

-- +goose Up
ALTER TABLE admin_playback_history ADD COLUMN subtitle_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE admin_playback_history DROP COLUMN subtitle_at;
