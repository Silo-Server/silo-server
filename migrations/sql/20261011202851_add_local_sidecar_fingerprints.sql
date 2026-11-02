-- +goose Up
-- Records the local sidecar files (NFO, artwork) last applied to an item,
-- season, or episode in one library, so scans and the scheduled refresh can
-- tell an edited sidecar from an unchanged one and re-apply only what changed.
-- content_id leads the key because silo_rename_content_ids moves these rows
-- with the item when its content ID changes.
CREATE TABLE local_sidecar_fingerprints (
    content_id      TEXT NOT NULL,
    target_type     TEXT NOT NULL CHECK (target_type IN ('item', 'season', 'episode')),
    -- The item the row belongs to: itself, or a season's or episode's series.
    -- Deleting the item drops its rows.
    item_id         TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE ON UPDATE CASCADE,
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    -- Hash of the applied sidecar files' paths, sizes and modification times.
    fingerprint     TEXT NOT NULL,
    -- Item rows: hash of every sidecar-type file in the item's folders, which a
    -- scan compares from its own directory listing before reading any sidecar.
    folder_digest   TEXT NOT NULL DEFAULT '',
    -- Item rows: the provider IDs the item's NFO declared when it was applied.
    provider_ids    JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(provider_ids) = 'object'),
    checked_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id, target_type, media_folder_id)
);

CREATE INDEX idx_local_sidecar_fingerprints_folder ON local_sidecar_fingerprints (media_folder_id);
CREATE INDEX idx_local_sidecar_fingerprints_item ON local_sidecar_fingerprints (item_id);

-- +goose Down
DROP TABLE local_sidecar_fingerprints;
