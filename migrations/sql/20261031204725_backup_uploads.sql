-- +goose Up
-- Restore sets uploaded from another server. Parts are written straight to
-- the shared operational store under backups/uploads/<id>/, so any API
-- server can take any part; this table is the session state every server
-- reads. A completed upload is frozen by its SHA-256 (digest).
CREATE TABLE public.backup_uploads (
    id uuid PRIMARY KEY,
    status text NOT NULL CHECK (status IN ('uploading', 'checking', 'complete', 'rejected')),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    part_size bigint NOT NULL CHECK (part_size > 0),
    declared_sha256 text NOT NULL DEFAULT '',
    digest text NOT NULL DEFAULT '',
    created_by_user_id integer,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    completed_at timestamp with time zone,
    source_server_id text NOT NULL DEFAULT '',
    source_server_name text NOT NULL DEFAULT '',
    source_silo_version text NOT NULL DEFAULT '',
    source_created_at timestamp with time zone,
    format_version integer NOT NULL DEFAULT 0,
    rejected_reason text NOT NULL DEFAULT '',
    rejected_message text NOT NULL DEFAULT ''
);

-- The object that counts for each received part. A part sent again is a new
-- object; the row moves to it in a transaction that requires the session to
-- be uploading, so a late writer can never change what completion read.
CREATE TABLE public.backup_upload_parts (
    upload_id uuid NOT NULL REFERENCES public.backup_uploads (id) ON DELETE CASCADE,
    part_number integer NOT NULL CHECK (part_number >= 0),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    object_name text NOT NULL,
    PRIMARY KEY (upload_id, part_number)
);

-- A part being written now: one per part, and a few per upload, taken under
-- the session row lock before any byte is stored, so concurrent sends of a
-- part cannot each write an object. A row older than the attempt limit is
-- abandoned and may be replaced.
CREATE TABLE public.backup_upload_attempts (
    upload_id uuid NOT NULL REFERENCES public.backup_uploads (id) ON DELETE CASCADE,
    part_number integer NOT NULL CHECK (part_number >= 0),
    object_name text NOT NULL,
    started_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (upload_id, part_number)
);

-- The lock holder can be checking an uploaded restore set (upload), and a
-- restore or that check names its upload.
ALTER TABLE public.backup_status ADD COLUMN active_upload_id text NOT NULL DEFAULT '';
ALTER TABLE public.backup_status DROP CONSTRAINT IF EXISTS backup_status_active_operation_check;
ALTER TABLE public.backup_status ADD CONSTRAINT backup_status_active_operation_check
    CHECK (active_operation IN ('', 'backup', 'delete', 'restore', 'upload'));

-- +goose Down
UPDATE public.backup_status SET active_operation = '' WHERE active_operation = 'upload';
ALTER TABLE public.backup_status DROP CONSTRAINT IF EXISTS backup_status_active_operation_check;
ALTER TABLE public.backup_status ADD CONSTRAINT backup_status_active_operation_check
    CHECK (active_operation IN ('', 'backup', 'delete', 'restore'));
ALTER TABLE public.backup_status DROP COLUMN IF EXISTS active_upload_id;
DROP TABLE IF EXISTS public.backup_upload_attempts;
DROP TABLE IF EXISTS public.backup_upload_parts;
DROP TABLE IF EXISTS public.backup_uploads;
