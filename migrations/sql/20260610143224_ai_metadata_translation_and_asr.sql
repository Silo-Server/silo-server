-- +goose Up
-- +goose StatementBegin
-- AI metadata translation jobs: on-demand (and per-library auto-fallback)
-- translation of overviews/taglines into the localization tables. Each row is
-- one job covering a media item (optionally expanded to its seasons/episodes),
-- one season, or one episode. Results are ordinary localization rows, so they
-- reach every client through the existing localized detail responses.
CREATE TABLE public.metadata_translation_jobs (
    id               bigserial   PRIMARY KEY,
    target_kind      text        NOT NULL,                   -- 'item' | 'season' | 'episode'
    content_id       text        NOT NULL,
    include_children boolean     NOT NULL DEFAULT true,      -- series item: include season/episode overviews
    source_language  text        NOT NULL DEFAULT '',
    target_language  text        NOT NULL,
    engine           text        NOT NULL DEFAULT 'openai',
    model            text        NOT NULL DEFAULT '',        -- snapshot of the model used, for provenance
    status           text        NOT NULL DEFAULT 'pending', -- pending|running|completed|failed|cancelled
    progress         double precision NOT NULL DEFAULT 0,    -- 0..1
    progress_message text        NOT NULL DEFAULT '',
    fields_done      integer     NOT NULL DEFAULT 0,
    fields_total     integer     NOT NULL DEFAULT 0,
    force            boolean     NOT NULL DEFAULT false,     -- overwrite provider/ai fields (never manual)
    error_message    text        NOT NULL DEFAULT '',
    idempotency_key  text        NOT NULL,
    requested_by     integer,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    heartbeat_at     timestamptz NOT NULL DEFAULT now()
);

-- Prevent duplicate in-flight jobs for the same work. Completed/failed/cancelled
-- rows do not block a re-run, so a user can retry after a failure.
CREATE UNIQUE INDEX metadata_translation_jobs_active_idempotency_idx
    ON public.metadata_translation_jobs (idempotency_key)
    WHERE status IN ('pending', 'running');

-- Listing recent jobs for an item (metadata editor progress UI, admin views).
CREATE INDEX metadata_translation_jobs_content_idx
    ON public.metadata_translation_jobs (content_id, created_at DESC);

-- Startup recovery scan for jobs left running by a crashed process.
CREATE INDEX metadata_translation_jobs_status_idx
    ON public.metadata_translation_jobs (status) WHERE status IN ('pending', 'running');

-- Field provenance on the AI-writable localization fields, so refreshes never
-- regress quality: 'manual' beats 'provider' beats 'ai'. A provider refresh
-- overwrites an AI translation; the reverse never happens.
ALTER TABLE media_item_localizations
    ADD COLUMN overview_source text NOT NULL DEFAULT 'provider'
        CHECK (overview_source IN ('provider', 'ai', 'manual')),
    ADD COLUMN tagline_source  text NOT NULL DEFAULT 'provider'
        CHECK (tagline_source IN ('provider', 'ai', 'manual'));
ALTER TABLE season_localizations
    ADD COLUMN overview_source text NOT NULL DEFAULT 'provider'
        CHECK (overview_source IN ('provider', 'ai', 'manual'));
ALTER TABLE episode_localizations
    ADD COLUMN overview_source text NOT NULL DEFAULT 'provider'
        CHECK (overview_source IN ('provider', 'ai', 'manual'));

-- Per-library opt-in: when metadata providers have no localization for the
-- library's metadata language, fall back to AI translation on refresh.
ALTER TABLE media_folders
    ADD COLUMN auto_translate_metadata boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_folders DROP COLUMN auto_translate_metadata;
ALTER TABLE episode_localizations DROP COLUMN overview_source;
ALTER TABLE season_localizations DROP COLUMN overview_source;
ALTER TABLE media_item_localizations DROP COLUMN tagline_source, DROP COLUMN overview_source;
DROP TABLE IF EXISTS public.metadata_translation_jobs;
-- +goose StatementEnd
