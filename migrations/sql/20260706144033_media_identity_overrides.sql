-- Path-scoped identity overrides for the split-versions flow (see
-- docs/superpowers/specs/2026-07-06-split-versions-reassign-design.md).
--
-- media_group_overrides forces an identity for an entire inferred group, which
-- cannot fix a wrong merge: the misgrouped files share one group key. These
-- overrides bind to *paths* instead — a root folder or a single file — and are
-- applied per file during group inference, before bucketing, so overridden
-- files form their own group and rescans converge on the corrected assignment.

-- +goose Up
CREATE TABLE media_identity_overrides (
    id                 bigserial PRIMARY KEY,
    media_folder_id    integer NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    scope              text NOT NULL CHECK (scope IN ('root', 'file')),
    root_path          text NOT NULL DEFAULT '',
    file_path          text NOT NULL DEFAULT '',
    forced_type        text NOT NULL DEFAULT '',
    forced_title       text NOT NULL DEFAULT '',
    forced_year        integer NOT NULL DEFAULT 0,
    forced_tmdb_id     text NOT NULL DEFAULT '',
    forced_imdb_id     text NOT NULL DEFAULT '',
    forced_tvdb_id     text NOT NULL DEFAULT '',
    note               text NOT NULL DEFAULT '',
    created_by_user_id integer REFERENCES users(id) ON DELETE SET NULL,
    updated_by_user_id integer REFERENCES users(id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT media_identity_overrides_scope_path CHECK (
        (scope = 'root' AND root_path <> '' AND file_path = '') OR
        (scope = 'file' AND file_path <> '' AND root_path = '')
    ),
    CONSTRAINT media_identity_overrides_unique UNIQUE (media_folder_id, scope, root_path, file_path)
);

-- +goose Down
DROP TABLE media_identity_overrides;
