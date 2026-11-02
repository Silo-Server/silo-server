-- Silo no longer supports Trakt. This removes the Trakt sources of
-- collections and home rows while keeping what a household built with them:
--
--   * Trakt-backed admin and personal collections become manual collections
--     that keep their current members; nothing syncs them again.
--   * Trakt trending home rows switch to TMDB weekly trending and are turned
--     off. A profile's own Trakt trending row is switched the same way and
--     hidden. Collection rows lose their Trakt marker and keep showing their
--     (now manual) collection.
--
-- The trending snapshot rows Trakt rows read are deleted. The SQLite user
-- store rewrites its profile section overrides the same way at schema v33.
-- 20261016134645_retire_trakt_watch_sync.sql removes the Trakt watch-sync
-- state.

-- +goose Up
UPDATE public.library_collections
   SET collection_type = 'manual',
       source_config = '{}'::jsonb,
       source_url = '',
       sync_schedule = NULL,
       next_sync_at = NULL,
       updated_at = now()
 WHERE collection_type = 'trakt'
    OR source_config->>'provider' = 'trakt'
    OR source_config->>'mode' LIKE 'trakt\_%';

ALTER TABLE public.library_collections DROP CONSTRAINT IF EXISTS library_collections_collection_type_check;
ALTER TABLE public.library_collections ADD CONSTRAINT library_collections_collection_type_check
    CHECK (collection_type = ANY (ARRAY['manual', 'smart', 'mdblist', 'tmdb']));

UPDATE public.user_personal_collections
   SET collection_type = 'manual',
       source_config = '{}'::jsonb,
       source_url = '',
       sync_schedule = NULL,
       next_sync_at = NULL,
       updated_at = now()
 WHERE collection_type = 'trakt'
    OR source_config->>'provider' = 'trakt'
    OR source_config->>'mode' LIKE 'trakt\_%';

ALTER TABLE public.user_personal_collections DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE public.user_personal_collections ADD CONSTRAINT user_personal_collections_type_check
    CHECK (collection_type IN ('manual', 'smart', 'mdblist', 'tmdb', 'synced', 'playlist'));

UPDATE public.page_sections
   SET config = config || '{"source": "tmdb", "window": "week"}'::jsonb,
       enabled = false,
       updated_at = now()
 WHERE section_type = 'trending_discover'
   AND config->>'source' = 'trakt';

UPDATE public.page_sections
   SET config = config - 'source_provider',
       updated_at = now()
 WHERE section_type = 'collection'
   AND config->>'source_provider' = 'trakt';

-- A profile's section overrides are one JSON array per page, stored in
-- user_settings under section_overrides:<profile>:<scope>[:<library>]. Each
-- element carries its row config in Config and, for a row the profile added,
-- UserConfig, both as JSON text. A value that does not parse is left alone.
-- +goose StatementBegin
CREATE FUNCTION pg_temp.retire_trakt_section_config(config text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
  parsed jsonb;
BEGIN
  IF config IS NULL OR config = '' THEN
    RETURN config;
  END IF;
  BEGIN
    parsed := config::jsonb;
  EXCEPTION WHEN invalid_text_representation THEN
    RETURN config;
  END;
  IF jsonb_typeof(parsed) <> 'object' THEN
    RETURN config;
  END IF;
  IF parsed->>'source' = 'trakt' THEN
    RETURN (parsed || '{"source": "tmdb", "window": "week"}'::jsonb)::text;
  END IF;
  IF parsed->>'source_provider' = 'trakt' THEN
    RETURN (parsed - 'source_provider')::text;
  END IF;
  RETURN config;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION pg_temp.is_trakt_trending_config(config text)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
AS $$
BEGIN
  IF config IS NULL OR config = '' THEN
    RETURN false;
  END IF;
  RETURN jsonb_typeof(config::jsonb) = 'object' AND config::jsonb->>'source' = 'trakt';
EXCEPTION WHEN invalid_text_representation THEN
  RETURN false;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION pg_temp.retire_trakt_section_overrides(stored text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
  overrides jsonb;
  element jsonb;
  rewritten jsonb := '[]'::jsonb;
  config text;
  user_config text;
BEGIN
  BEGIN
    overrides := stored::jsonb;
  EXCEPTION WHEN invalid_text_representation THEN
    RETURN stored;
  END;
  IF jsonb_typeof(overrides) <> 'array' THEN
    RETURN stored;
  END IF;
  FOR element IN SELECT value FROM jsonb_array_elements(overrides) WITH ORDINALITY ORDER BY ordinality LOOP
    IF jsonb_typeof(element) = 'object' THEN
      config := element->>'Config';
      user_config := element->>'UserConfig';
      -- A profile's own Trakt trending row is hidden, as the admin rows are
      -- turned off; a row linked to an admin row follows that row.
      IF COALESCE(element->>'SectionID', '') = ''
         AND (pg_temp.is_trakt_trending_config(config) OR pg_temp.is_trakt_trending_config(user_config)) THEN
        element := element || '{"Hidden": true}'::jsonb;
      END IF;
      IF jsonb_typeof(element->'Config') = 'string' THEN
        element := jsonb_set(element, '{Config}', to_jsonb(pg_temp.retire_trakt_section_config(config)));
      END IF;
      IF jsonb_typeof(element->'UserConfig') = 'string' THEN
        element := jsonb_set(element, '{UserConfig}', to_jsonb(pg_temp.retire_trakt_section_config(user_config)));
      END IF;
    END IF;
    rewritten := rewritten || jsonb_build_array(element);
  END LOOP;
  IF rewritten = overrides THEN
    RETURN stored;
  END IF;
  RETURN rewritten::text;
END;
$$;
-- +goose StatementEnd

UPDATE public.user_settings
   SET value = pg_temp.retire_trakt_section_overrides(value)
 WHERE key LIKE 'section\_overrides:%'
   AND value LIKE '%trakt%'
   AND pg_temp.retire_trakt_section_overrides(value) IS DISTINCT FROM value;

DROP FUNCTION pg_temp.retire_trakt_section_overrides(text);
DROP FUNCTION pg_temp.is_trakt_trending_config(text);
DROP FUNCTION pg_temp.retire_trakt_section_config(text);

DELETE FROM public.trending_discover_snapshots WHERE source = 'trakt';

-- +goose Down
-- Irreversible: the Trakt sources are not recorded anywhere. An older server
-- keeps the converted collections and rows as manual collections and TMDB
-- trending rows.
ALTER TABLE public.library_collections DROP CONSTRAINT IF EXISTS library_collections_collection_type_check;
ALTER TABLE public.library_collections ADD CONSTRAINT library_collections_collection_type_check
    CHECK (collection_type = ANY (ARRAY['manual', 'smart', 'mdblist', 'tmdb', 'trakt']));

ALTER TABLE public.user_personal_collections DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE public.user_personal_collections ADD CONSTRAINT user_personal_collections_type_check
    CHECK (collection_type IN ('manual', 'smart', 'mdblist', 'tmdb', 'trakt', 'synced', 'playlist'));
