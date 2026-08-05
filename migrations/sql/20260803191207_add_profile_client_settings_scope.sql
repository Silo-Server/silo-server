-- Add a profile-and-client-family identity to the canonical settings store.
-- Unlike device_platform, client_family is a closed request-supplied enum and
-- therefore safe to use as part of resolution and uniqueness.

-- +goose Up
ALTER TABLE public.user_setting_values
    ADD COLUMN client_family text;

ALTER TABLE public.user_setting_values
    DROP CONSTRAINT user_setting_values_scope_check,
    DROP CONSTRAINT user_setting_values_identity_check,
    ADD CONSTRAINT user_setting_values_client_family_check
        CHECK (client_family IS NULL OR client_family IN ('tv', 'mobile', 'tablet', 'desktop', 'web')),
    ADD CONSTRAINT user_setting_values_scope_check
        CHECK (scope IN ('account', 'profile', 'profile_client', 'profile_device', 'profile_library', 'profile_series')),
    ADD CONSTRAINT user_setting_values_identity_check CHECK (
      (scope = 'account' AND profile_id IS NULL AND client_family IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile' AND profile_id IS NOT NULL AND client_family IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_client' AND profile_id IS NOT NULL AND client_family IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_device' AND profile_id IS NOT NULL AND client_family IS NULL AND device_id IS NOT NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_library' AND profile_id IS NOT NULL AND client_family IS NULL AND device_id IS NULL AND library_id IS NOT NULL AND series_id IS NULL) OR
      (scope = 'profile_series' AND profile_id IS NOT NULL AND client_family IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NOT NULL)
    );

CREATE UNIQUE INDEX user_setting_values_profile_client_uq
  ON public.user_setting_values (user_id, profile_id, client_family, key)
  WHERE scope = 'profile_client';

-- Decode either the canonical object or the one-layer JSON-string encoding
-- written by legacy web clients. Invalid string contents return NULL instead
-- of aborting the whole migration.
-- +goose StatementBegin
CREATE FUNCTION pg_temp.decode_legacy_sidebar_pins(candidate jsonb)
RETURNS jsonb
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
  decoded jsonb;
BEGIN
  IF jsonb_typeof(candidate) = 'object' THEN
    RETURN candidate;
  END IF;
  IF jsonb_typeof(candidate) = 'string' THEN
    BEGIN
      decoded := (candidate #>> '{}')::jsonb;
    EXCEPTION WHEN invalid_text_representation THEN
      RETURN NULL;
    END;

    IF jsonb_typeof(decoded) = 'object' THEN
      RETURN decoded;
    END IF;
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Seed the new profile-wide shortcut catalog from convertible legacy web
-- sidebar pins. ui.sidebar_pins remains untouched, rows with non-numeric
-- well-known groups remain there, and an already-authored nav.shortcuts row is
-- never overwritten. The new schema bounds the catalog at 256 entries.
WITH legacy AS (
  SELECT value_row.id AS legacy_id,
         value_row.user_id,
         value_row.profile_id,
         value_row.created_at,
         value_row.updated_at,
         decoded.value
    FROM public.user_setting_values AS value_row
    CROSS JOIN LATERAL (
      SELECT pg_temp.decode_legacy_sidebar_pins(value_row.value) AS value
    ) AS decoded
   WHERE value_row.key = 'ui.sidebar_pins'
     AND value_row.scope = 'profile'
     AND decoded.value IS NOT NULL
     AND NOT EXISTS (
       SELECT 1
         FROM public.user_setting_values AS current
        WHERE current.user_id = value_row.user_id
          AND current.profile_id = value_row.profile_id
          AND current.key = 'nav.shortcuts'
          AND current.scope = 'profile'
     )
), library_groups AS (
  SELECT legacy.legacy_id,
         legacy.user_id,
         legacy.profile_id,
         legacy.created_at,
         legacy.updated_at,
         groups.pins,
         CASE
           WHEN groups.group_key ~ '^[1-9][0-9]{0,9}$' THEN
             CASE
               WHEN groups.group_key::bigint <= 2147483647 THEN groups.group_key::integer
             END
         END AS library_id
    FROM legacy
    CROSS JOIN LATERAL jsonb_each(legacy.value) AS groups(group_key, pins)
), valid_pins AS (
  SELECT group_row.legacy_id,
         group_row.user_id,
         group_row.profile_id,
         group_row.created_at,
         group_row.updated_at,
         group_row.library_id,
         pin.pin_value->>'type' AS pin_type,
         pin.pin_value->>'id' AS pin_id,
         pin.pin_value->>'label' AS pin_label,
         pin.ordinality
    FROM library_groups AS group_row
    CROSS JOIN LATERAL jsonb_array_elements(
      CASE WHEN jsonb_typeof(group_row.pins) = 'array' THEN group_row.pins ELSE '[]'::jsonb END
    ) WITH ORDINALITY AS pin(pin_value, ordinality)
   WHERE group_row.library_id IS NOT NULL
     AND jsonb_typeof(pin.pin_value) = 'object'
     AND pin.pin_value->>'type' IN ('section', 'collection')
     AND jsonb_typeof(pin.pin_value->'id') = 'string'
     AND jsonb_typeof(pin.pin_value->'label') = 'string'
     AND char_length(COALESCE(pin.pin_value->>'id', '')) BETWEEN 1 AND 128
     AND pin.pin_value->>'id' ~ '[^[:space:]]'
     AND char_length(COALESCE(pin.pin_value->>'label', '')) BETWEEN 1 AND 256
     AND pin.pin_value->>'label' ~ '[^[:space:]]'
), deduplicated AS (
  SELECT valid_pins.*,
         row_number() OVER (
           PARTITION BY legacy_id, library_id, pin_type, pin_id
           ORDER BY ordinality
         ) AS duplicate_rank
    FROM valid_pins
), ranked AS (
  SELECT deduplicated.*,
         row_number() OVER (
           PARTITION BY legacy_id
           ORDER BY library_id, ordinality, pin_type, pin_id
         ) AS item_rank
    FROM deduplicated
   WHERE duplicate_rank = 1
), shortcut_values AS (
  SELECT legacy_id,
         user_id,
         profile_id,
         created_at,
         updated_at,
         jsonb_build_object(
           'items',
           jsonb_agg(
             CASE pin_type
               WHEN 'section' THEN jsonb_build_object(
                 'type', 'section',
                 'library_id', library_id,
                 'section_id', pin_id,
                 'label', pin_label
               )
               ELSE jsonb_build_object(
                 'type', 'collection',
                 'library_id', library_id,
                 'collection_id', pin_id,
                 'label', pin_label
               )
             END
             ORDER BY item_rank
           )
         ) AS value
    FROM ranked
   WHERE item_rank <= 256
   GROUP BY legacy_id, user_id, profile_id, created_at, updated_at
)
INSERT INTO public.user_setting_values
    (user_id, key, scope, profile_id, client_family, device_id, library_id, series_id,
     value, revision, created_at, updated_at)
SELECT user_id, 'nav.shortcuts', 'profile', profile_id, NULL, NULL, NULL, NULL,
       value, 1, created_at, updated_at
  FROM shortcut_values
ON CONFLICT (user_id, profile_id, key) WHERE scope = 'profile' DO NOTHING;

DROP FUNCTION pg_temp.decode_legacy_sidebar_pins(jsonb);

-- +goose Down
DELETE FROM public.user_setting_values WHERE scope = 'profile_client';

DROP INDEX IF EXISTS public.user_setting_values_profile_client_uq;

ALTER TABLE public.user_setting_values
    DROP CONSTRAINT user_setting_values_client_family_check,
    DROP CONSTRAINT user_setting_values_scope_check,
    DROP CONSTRAINT user_setting_values_identity_check,
    ADD CONSTRAINT user_setting_values_scope_check
        CHECK (scope IN ('account', 'profile', 'profile_device', 'profile_library', 'profile_series')),
    ADD CONSTRAINT user_setting_values_identity_check CHECK (
      (scope = 'account' AND profile_id IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_device' AND profile_id IS NOT NULL AND device_id IS NOT NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_library' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NOT NULL AND series_id IS NULL) OR
      (scope = 'profile_series' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NOT NULL)
    );

ALTER TABLE public.user_setting_values DROP COLUMN client_family;
