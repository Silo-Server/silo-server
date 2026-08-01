-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.virtual_media_source_claims (
    plugin_installation_id bigint NOT NULL CHECK (plugin_installation_id >= 0),
    source_key text NOT NULL CHECK (
        source_key <> ''
        AND octet_length(source_key) <= 128
        AND source_key = btrim(source_key)
    ),
    content_id text NOT NULL REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    media_folder_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    owns_item_metadata boolean NOT NULL DEFAULT false,
    last_seen_at timestamp with time zone NOT NULL DEFAULT now(),
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_installation_id, source_key, content_id, media_folder_id)
);

CREATE INDEX idx_virtual_media_source_claims_content
    ON public.virtual_media_source_claims(content_id);

CREATE TABLE public.virtual_media_file_source_claims (
    plugin_installation_id bigint NOT NULL,
    source_key text NOT NULL,
    content_id text NOT NULL,
    media_folder_id integer NOT NULL,
    file_path text NOT NULL CHECK (
        file_path LIKE 'virtual://%'
        AND octet_length(file_path) <= 1024
    ),
    last_seen_at timestamp with time zone NOT NULL DEFAULT now(),
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin_installation_id, source_key, content_id, media_folder_id, file_path),
    FOREIGN KEY (plugin_installation_id, source_key, content_id, media_folder_id)
        REFERENCES public.virtual_media_source_claims(
            plugin_installation_id, source_key, content_id, media_folder_id
        ) ON DELETE CASCADE
);

CREATE INDEX idx_virtual_media_file_source_claims_file
    ON public.virtual_media_file_source_claims(plugin_installation_id, file_path);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.media_items mi
        WHERE mi.virtual_owner_installation_id IS NOT NULL
          AND (
            octet_length(COALESCE(NULLIF(btrim(mi.virtual_source), ''), 'plugin')) > 128
            OR mi.virtual_source <> btrim(mi.virtual_source)
          )
    ) THEN
        RAISE EXCEPTION 'cannot backfill virtual source claims: existing source key is not canonical';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM public.media_files mf
        WHERE (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
          AND octet_length(mf.file_path) > 1024
    ) THEN
        RAISE EXCEPTION 'cannot backfill virtual source claims: existing virtual path exceeds 1024 bytes';
    END IF;
END $$;

INSERT INTO public.virtual_media_source_claims(
    plugin_installation_id, source_key, content_id, media_folder_id,
    owns_item_metadata, last_seen_at, created_at, updated_at
)
SELECT DISTINCT
    COALESCE(NULLIF(mf.virtual_owner_installation_id, 0), mi.virtual_owner_installation_id),
    COALESCE(NULLIF(btrim(mi.virtual_source), ''), 'plugin'),
    mi.content_id,
    mf.media_folder_id,
    true,
    COALESCE(mi.virtual_last_seen_at, mf.updated_at, now()),
    COALESCE(mi.created_at, now()),
    now()
FROM public.media_items mi
JOIN public.media_files mf ON mf.content_id = mi.content_id
WHERE (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
  AND COALESCE(NULLIF(mf.virtual_owner_installation_id, 0), mi.virtual_owner_installation_id) IS NOT NULL
ON CONFLICT(plugin_installation_id, source_key, content_id, media_folder_id) DO UPDATE SET
    owns_item_metadata = true,
    last_seen_at = GREATEST(
        public.virtual_media_source_claims.last_seen_at,
        EXCLUDED.last_seen_at
    ),
    updated_at = now();

INSERT INTO public.virtual_media_source_claims(
    plugin_installation_id, source_key, content_id, media_folder_id,
    owns_item_metadata, last_seen_at, created_at, updated_at
)
SELECT
    mi.virtual_owner_installation_id,
    COALESCE(NULLIF(btrim(mi.virtual_source), ''), 'plugin'),
    mi.content_id,
    mil.media_folder_id,
    true,
    COALESCE(mi.virtual_last_seen_at, now()),
    COALESCE(mi.created_at, now()),
    now()
FROM public.media_items mi
JOIN public.media_item_libraries mil ON mil.content_id = mi.content_id
WHERE mi.virtual_owner_installation_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM public.virtual_media_source_claims existing
    WHERE existing.plugin_installation_id = mi.virtual_owner_installation_id
      AND existing.source_key = COALESCE(NULLIF(btrim(mi.virtual_source), ''), 'plugin')
      AND existing.content_id = mi.content_id
      AND existing.media_folder_id = mil.media_folder_id
  )
ON CONFLICT DO NOTHING;

INSERT INTO public.virtual_media_file_source_claims(
    plugin_installation_id, source_key, content_id, media_folder_id,
    file_path, last_seen_at, created_at, updated_at
)
SELECT
    claims.plugin_installation_id,
    claims.source_key,
    claims.content_id,
    claims.media_folder_id,
    mf.file_path,
    GREATEST(claims.last_seen_at, COALESCE(mf.updated_at, claims.last_seen_at)),
    COALESCE(mf.created_at, now()),
    now()
FROM public.virtual_media_source_claims claims
JOIN public.media_files mf
  ON mf.content_id = claims.content_id
 AND mf.media_folder_id = claims.media_folder_id
WHERE (mf.container = 'virtual' OR mf.file_path LIKE 'virtual://%')
  AND (
    mf.virtual_owner_installation_id = claims.plugin_installation_id
    OR (
        mf.virtual_owner_installation_id = 0
        AND claims.owns_item_metadata
    )
  )
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT content_id
        FROM public.virtual_media_source_claims
        GROUP BY content_id
        HAVING count(DISTINCT (plugin_installation_id, source_key)) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot safely downgrade virtual media source claims: multi-source items exist';
    END IF;
END $$;

UPDATE public.media_items mi
SET
    virtual_owner_installation_id = claim.plugin_installation_id,
    virtual_source = claim.source_key,
    virtual_last_seen_at = claim.last_seen_at,
    updated_at = now()
FROM (
    SELECT DISTINCT ON (content_id)
        content_id, plugin_installation_id, source_key, last_seen_at
    FROM public.virtual_media_source_claims
    WHERE owns_item_metadata
    ORDER BY content_id, last_seen_at DESC
) claim
WHERE claim.content_id = mi.content_id;

DROP TABLE public.virtual_media_file_source_claims;
DROP TABLE public.virtual_media_source_claims;
-- +goose StatementEnd
