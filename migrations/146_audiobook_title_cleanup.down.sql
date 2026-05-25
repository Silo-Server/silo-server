-- Restore titles from original_title when the cleanup ran on this row.
-- We can't perfectly identify which rows were cleaned (the clean+raw
-- mapping isn't logged), so we restore any audiobook whose
-- original_title is set and whose title doesn't already include the
-- narrator suffix.
UPDATE media_items mi
SET
    title          = mi.original_title,
    sort_title     = LOWER(mi.original_title),
    original_title = '',
    updated_at     = NOW()
WHERE mi.type = 'audiobook'
  AND COALESCE(mi.original_title, '') <> ''
  AND mi.original_title <> mi.title;
