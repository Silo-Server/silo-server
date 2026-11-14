-- +goose Up
-- A movie, series or mixed library catalogs files by extension, and some of
-- them hold no video stream (a soundtrack saved as .mkv). The scanner leaves
-- such a file out of media_files and records here the bytes it judged, so
-- later scans skip the file without probing it again and a failed probe
-- cannot bring a retired row back. A verdict holds only while the file at the
-- path still has the recorded size and modification time.
--
-- The scanner rebuilds this table: a deleted row costs one probe of that file.
CREATE TABLE public.scanner_no_video_files (
    file_path        text        PRIMARY KEY,
    media_folder_id  integer     NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    file_size        bigint      NOT NULL,
    file_modified_at timestamptz NOT NULL,
    file_hash        text        NOT NULL DEFAULT ''
);

-- A renamed file is looked up by its bytes when its probe fails.
CREATE INDEX scanner_no_video_files_bytes_idx
    ON public.scanner_no_video_files (media_folder_id, file_hash, file_size)
    WHERE file_hash <> '';

-- +goose Down
DROP TABLE public.scanner_no_video_files;
