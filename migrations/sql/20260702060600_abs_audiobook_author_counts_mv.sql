-- +goose Up
-- +goose StatementBegin
-- Precomputed audiobook author list for the ABS-compat /libraries/{id}/authors
-- endpoint. The live query GROUP BYs all authors of a 100K+ library on every
-- page (~800ms) plus a COUNT(DISTINCT) (~500ms); paging the full author list
-- for an iOS sync then blows past the background-task window. This MV turns the
-- list into an indexed paginated read. added_at = people.created_at (= real ABS
-- author.createdAt) so clients can sort newest-first and run incremental syncs.
-- Refreshed periodically by the audiobooks service (REFRESH ... CONCURRENTLY).
CREATE MATERIALIZED VIEW IF NOT EXISTS abs_audiobook_author_counts AS
SELECT
    p.id                  AS person_id,
    p.name                AS name,
    mil.media_folder_id   AS library_id,
    COUNT(DISTINCT mi.content_id) AS num_books,
    p.created_at          AS added_at
FROM media_items mi
JOIN media_item_libraries mil ON mil.content_id = mi.content_id
JOIN item_people ip ON ip.content_id = mi.content_id AND ip.kind = 7
JOIN people p ON p.id = ip.person_id
WHERE mi.type = 'audiobook'
GROUP BY p.id, p.name, mil.media_folder_id, p.created_at
WITH DATA;

-- Unique index is required for REFRESH MATERIALIZED VIEW CONCURRENTLY.
CREATE UNIQUE INDEX IF NOT EXISTS idx_abs_author_counts_pk
    ON abs_audiobook_author_counts (library_id, person_id);
-- Name sort (default ABS author ordering).
CREATE INDEX IF NOT EXISTS idx_abs_author_counts_name
    ON abs_audiobook_author_counts (library_id, LOWER(name));
-- addedAt-desc sort for client incremental syncs.
CREATE INDEX IF NOT EXISTS idx_abs_author_counts_added
    ON abs_audiobook_author_counts (library_id, added_at DESC, person_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP MATERIALIZED VIEW IF EXISTS abs_audiobook_author_counts;
-- +goose StatementEnd
