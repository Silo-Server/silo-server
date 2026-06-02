-- Restore per-fulfillment columns on media_requests.
ALTER TABLE public.media_requests
    ADD COLUMN IF NOT EXISTS integration_kind text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS external_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS external_status text NOT NULL DEFAULT '';

-- Copy back the 1080p target's fulfillment fields (lossy: 4K/anime targets dropped).
UPDATE public.media_requests mr
SET integration_kind = t.integration_kind,
    external_id = t.external_id,
    external_status = t.external_status
FROM public.media_request_targets t
WHERE t.request_id = mr.id AND t.quality = '1080p';

ALTER TABLE public.media_requests DROP COLUMN IF EXISTS is_anime;

DROP TABLE IF EXISTS public.media_request_targets;

-- Collapse request_integrations back to kind-PK (lossy: keep one default per kind).
DELETE FROM public.request_integrations a
USING public.request_integrations b
WHERE a.kind = b.kind AND a.id <> b.id AND b.is_default AND NOT a.is_default;
-- If a kind has no default, keep an arbitrary row and drop the rest.
DELETE FROM public.request_integrations a
USING public.request_integrations b
WHERE a.kind = b.kind AND a.ctid < b.ctid;

DROP INDEX IF EXISTS idx_request_integrations_default_per_kind;
DROP INDEX IF EXISTS idx_request_integrations_default4k_per_kind;

ALTER TABLE public.request_integrations DROP CONSTRAINT request_integrations_pkey;
ALTER TABLE public.request_integrations ADD PRIMARY KEY (kind);
ALTER TABLE public.request_integrations
    DROP COLUMN IF EXISTS id,
    DROP COLUMN IF EXISTS name,
    DROP COLUMN IF EXISTS is_4k,
    DROP COLUMN IF EXISTS is_default,
    DROP COLUMN IF EXISTS is_default_4k,
    DROP COLUMN IF EXISTS anime_enabled,
    DROP COLUMN IF EXISTS anime_quality_profile_id,
    DROP COLUMN IF EXISTS anime_root_folder,
    DROP COLUMN IF EXISTS anime_tags;
