-- +goose Up
-- +goose StatementBegin
-- Persist the H.264 multi-PPS copy-safety verdict so the bitstream scan is not
-- re-run on every process restart. The verdict is self-validating: it is only
-- trusted when the recorded scan size and mtime still match the media_files
-- row, so any rewrite of the file invalidates it without writer coordination.
ALTER TABLE public.media_files
    ADD COLUMN multiple_pps boolean,
    ADD COLUMN multiple_pps_scan_size bigint,
    ADD COLUMN multiple_pps_scan_mtime timestamptz;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.media_files
    DROP COLUMN IF EXISTS multiple_pps_scan_mtime,
    DROP COLUMN IF EXISTS multiple_pps_scan_size,
    DROP COLUMN IF EXISTS multiple_pps;
-- +goose StatementEnd
