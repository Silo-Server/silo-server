-- Home and library pages no longer take a Next Up (next_up) row that an
-- admin or a profile added. The only Next Up row is the one the server
-- inserts after Continue Watching when a profile's ui.next_up_mode is
-- separate. An added one showed nothing in the combined mode, where Continue
-- Watching carries next episodes, and repeated that row in the separate mode.
-- This deletes the added rows; the server now refuses new ones. The SQLite
-- user store deletes its profile rows the same way at schema v34.

-- +goose Up

-- A profile's section overrides are one JSON array per page, stored in
-- user_settings under section_overrides:<scope>:<library>. A row the profile
-- added has no SectionID and runs UserSectionType, else the legacy
-- SectionType. Overrides of an admin row keep their SectionID: the deleted
-- admin rows' overrides then match nothing, as after any admin row delete.
-- An element is dropped only when it is positively a next_up row: one with no
-- recipe fields at all keeps its place. A value that does not parse is left
-- alone; one left empty is deleted, as
-- the store does when a save leaves a page with no overrides.
-- +goose StatementBegin
CREATE FUNCTION pg_temp.retire_next_up_section_overrides(stored text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
  overrides jsonb;
  kept jsonb;
BEGIN
  BEGIN
    overrides := stored::jsonb;
  EXCEPTION WHEN invalid_text_representation THEN
    RETURN stored;
  END;
  IF jsonb_typeof(overrides) <> 'array' THEN
    RETURN stored;
  END IF;
  SELECT COALESCE(jsonb_agg(element ORDER BY ordinality), '[]'::jsonb)
    INTO kept
    FROM jsonb_array_elements(overrides) WITH ORDINALITY AS e(element, ordinality)
   WHERE (
           jsonb_typeof(element) = 'object'
           AND COALESCE(element->>'SectionID', '') = ''
           AND COALESCE(NULLIF(element->>'UserSectionType', ''), element->>'SectionType') = 'next_up'
         ) IS NOT TRUE;
  IF jsonb_array_length(kept) = jsonb_array_length(overrides) THEN
    RETURN stored;
  END IF;
  RETURN kept::text;
END;
$$;
-- +goose StatementEnd

UPDATE public.user_settings
   SET value = pg_temp.retire_next_up_section_overrides(value)
 WHERE key LIKE 'section\_overrides:%'
   AND value LIKE '%next\_up%'
   AND pg_temp.retire_next_up_section_overrides(value) NOT IN (value, '[]');

DELETE FROM public.user_settings
 WHERE key LIKE 'section\_overrides:%'
   AND value LIKE '%next\_up%'
   AND pg_temp.retire_next_up_section_overrides(value) = '[]';

DROP FUNCTION pg_temp.retire_next_up_section_overrides(text);

DELETE FROM public.page_sections WHERE section_type = 'next_up';

-- +goose Down
-- Irreversible: the deleted rows are not recorded anywhere. An older server
-- shows pages without them.
