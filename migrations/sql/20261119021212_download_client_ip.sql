-- +goose Up
-- Where a download was registered from and where its device was last seen,
-- as the trusted-proxy resolution (internal/clientip) reads the request.
-- last_client_ip_at is when last_client_ip was last written; it bounds how
-- often file requests rewrite it. Rows registered before this column keep
-- NULL.
ALTER TABLE public.downloads
    ADD COLUMN created_client_ip inet,
    ADD COLUMN last_client_ip inet,
    ADD COLUMN last_client_ip_at timestamptz;

-- +goose Down
ALTER TABLE public.downloads
    DROP COLUMN IF EXISTS last_client_ip_at,
    DROP COLUMN IF EXISTS last_client_ip,
    DROP COLUMN IF EXISTS created_client_ip;
