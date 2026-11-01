-- +goose Up
-- +goose StatementBegin
-- A request asks for one quality: a regular request gets the HD copy (and a
-- 4K copy too while force_dual_quality is on), a 4K request gets the 4K copy
-- alone. A title can have one open request of each, so the active-request
-- index keys on the quality as well. Requests made before this keep their
-- targets and count as regular requests.
ALTER TABLE public.media_requests
    ADD COLUMN IF NOT EXISTS is_4k boolean NOT NULL DEFAULT false;

DROP INDEX IF EXISTS public.idx_media_requests_active_tmdb;
CREATE UNIQUE INDEX IF NOT EXISTS idx_media_requests_active_tmdb
    ON public.media_requests (media_type, provider, tmdb_id, is_4k)
    WHERE outcome = 'active' AND status <> 'completed';

-- Who may make 4K requests resolves like the quota and approval: the
-- account's own setting, then its access group's, then the server-wide
-- default, which starts off.
ALTER TABLE public.request_settings
    ADD COLUMN IF NOT EXISTS uhd_requests_allowed boolean NOT NULL DEFAULT false;

ALTER TABLE public.request_user_limits
    ADD COLUMN IF NOT EXISTS uhd_mode text NOT NULL DEFAULT 'inherit'
        CONSTRAINT request_user_limits_uhd_mode_check CHECK (uhd_mode IN ('inherit', 'allowed', 'denied'));

ALTER TABLE public.request_group_limits
    ADD COLUMN IF NOT EXISTS uhd_mode text NOT NULL DEFAULT 'inherit'
        CONSTRAINT request_group_limits_uhd_mode_check CHECK (uhd_mode IN ('inherit', 'allowed', 'denied'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Lossy: 4K requests are deleted, since the title-only index cannot hold a
-- regular and a 4K request for the same title.
DELETE FROM public.media_requests WHERE is_4k;

ALTER TABLE public.request_group_limits DROP COLUMN IF EXISTS uhd_mode;
ALTER TABLE public.request_user_limits DROP COLUMN IF EXISTS uhd_mode;
ALTER TABLE public.request_settings DROP COLUMN IF EXISTS uhd_requests_allowed;

DROP INDEX IF EXISTS public.idx_media_requests_active_tmdb;
CREATE UNIQUE INDEX IF NOT EXISTS idx_media_requests_active_tmdb
    ON public.media_requests (media_type, provider, tmdb_id)
    WHERE outcome = 'active' AND status <> 'completed';

ALTER TABLE public.media_requests DROP COLUMN IF EXISTS is_4k;
-- +goose StatementEnd
