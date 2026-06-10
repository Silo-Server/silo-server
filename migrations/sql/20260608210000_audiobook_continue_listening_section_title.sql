-- +goose Up
-- +goose StatementBegin
UPDATE page_sections ps
SET title = 'Continue Listening',
    updated_at = NOW()
FROM media_folders mf
WHERE ps.scope = 'library'
  AND ps.library_id = mf.id
  AND lower(mf.type) IN ('audiobook', 'audiobooks')
  AND ps.section_type = 'continue_watching'
  AND ps.position = 0
  AND ps.title = 'Continue Watching';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE page_sections ps
SET title = 'Continue Watching',
    updated_at = NOW()
FROM media_folders mf
WHERE ps.scope = 'library'
  AND ps.library_id = mf.id
  AND lower(mf.type) IN ('audiobook', 'audiobooks')
  AND ps.section_type = 'continue_watching'
  AND ps.position = 0
  AND ps.title = 'Continue Listening';
-- +goose StatementEnd
