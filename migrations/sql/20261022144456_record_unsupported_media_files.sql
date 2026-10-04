-- +goose Up
-- Files a library scan found but does not catalog because of their type (DVD
-- VOBs, disc images, RealMedia, Blu-ray disc streams), kept so an admin can
-- see why a title never appeared. One row per directory and reason: a disc
-- folder holds dozens of such files, and the directory is what an admin acts
-- on. A library scan replaces the library's rows and a subtree scan the rows
-- under its subtree. reason is one of the scanner's UnsupportedReason codes.
-- file_names holds the files' names in name order, at most 100
-- (scanner.MaxUnsupportedFileNames); file_count counts them all.
CREATE TABLE public.unsupported_media_files (
    media_folder_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    directory_path text NOT NULL,
    reason text NOT NULL,
    file_count integer NOT NULL CHECK (file_count > 0),
    file_names text[] NOT NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (media_folder_id, directory_path, reason)
);

-- +goose Down
DROP TABLE public.unsupported_media_files;
