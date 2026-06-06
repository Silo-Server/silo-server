-- Clean narrator/edition suffixes out of audiobook titles. The narrator
-- is already captured separately in item_people (kind=8) from the file's
-- narrator tag, so leaving it in the title field is duplicate data that
-- visibly clutters the UI ("Cytonic (UK Version: Read by Sophie Aldred)"
-- → "Cytonic"). The raw title is preserved in original_title for
-- forensic reference.
--
-- The regex must stay in lockstep with the Go scanner.stripNarratorSuffix
-- helper. Both run case-insensitive and target a trailing block of the
-- form: optional separator + optional "(UK Version:|US Version:|...|)"
-- + "read by X" + optional close-paren.

WITH cleaned AS (
    SELECT mi.content_id,
           mi.title AS raw_title,
           trim(regexp_replace(
               regexp_replace(
                   regexp_replace(
                       mi.title,
                       E'\\s*\\(?\\s*[-:,]?\\s*(UK Version:?|US Version:?)?\\s*[Rr]ead [Bb]y [A-Za-z0-9., ''&]+\\)?\\s*$',
                       '',
                       'g'
                   ),
                   E'\\s*\\(unabridged\\)\\s*', ' ', 'gi'
               ),
               E'\\s+', ' ', 'g'
           )) AS clean_title
    FROM media_items mi
    WHERE mi.type = 'audiobook'
      AND (mi.title ~* '\s+read by ' OR mi.title ~* '\(unabridged\)')
)
UPDATE media_items mi
SET
    title          = c.clean_title,
    sort_title     = LOWER(c.clean_title),
    original_title = CASE
                         WHEN COALESCE(mi.original_title, '') = '' THEN c.raw_title
                         ELSE mi.original_title
                     END,
    updated_at     = NOW()
FROM cleaned c
WHERE mi.content_id = c.content_id
  AND c.clean_title <> ''
  AND c.clean_title <> c.raw_title;
