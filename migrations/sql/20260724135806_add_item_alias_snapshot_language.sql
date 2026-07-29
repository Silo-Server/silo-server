-- +goose Up
-- An alias can describe one language while belonging to a snapshot requested
-- in another language. Track the request scope separately so replacing a
-- complete snapshot removes every alias that snapshot previously supplied.
-- NULL is reserved for rows that predate request-scope tracking; every new
-- writer supplies either a normalized language or '' for a provider-wide
-- snapshot.
ALTER TABLE media_item_aliases
    ADD COLUMN snapshot_language text;

DROP INDEX idx_media_item_aliases_unique;
CREATE UNIQUE INDEX idx_media_item_aliases_unique
    ON media_item_aliases (
        content_id,
        normalized_title,
        language,
        kind,
        provider,
        snapshot_language
    );

-- +goose Down
-- Multiple request scopes may contain the same public alias. Keep one before
-- restoring the original uniqueness definition.
DELETE FROM media_item_aliases duplicate
USING media_item_aliases keeper
WHERE duplicate.id > keeper.id
  AND duplicate.content_id = keeper.content_id
  AND duplicate.normalized_title = keeper.normalized_title
  AND duplicate.language = keeper.language
  AND duplicate.kind = keeper.kind
  AND duplicate.provider = keeper.provider;

DROP INDEX idx_media_item_aliases_unique;
ALTER TABLE media_item_aliases
    DROP COLUMN snapshot_language;
CREATE UNIQUE INDEX idx_media_item_aliases_unique
    ON media_item_aliases (content_id, normalized_title, language, kind, provider);
