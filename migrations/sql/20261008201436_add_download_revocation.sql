-- +goose Up
-- An administrator can revoke a device's downloads. The app deletes its local
-- copy at its next sync and confirms with DELETE, which removes the row. A row
-- the device never confirms is pruned 90 days after revoked_at.
ALTER TABLE public.downloads
    ADD COLUMN revoked_at     timestamptz,
    ADD COLUMN revoked_by     integer REFERENCES public.users(id) ON DELETE SET NULL,
    ADD COLUMN revoked_reason text NOT NULL DEFAULT '';

CREATE INDEX downloads_revoked_at_idx ON public.downloads (revoked_at) WHERE status = 'revoked';

-- +goose Down
DROP INDEX IF EXISTS public.downloads_revoked_at_idx;
ALTER TABLE public.downloads
    DROP COLUMN IF EXISTS revoked_reason,
    DROP COLUMN IF EXISTS revoked_by,
    DROP COLUMN IF EXISTS revoked_at;
