-- +goose Up
-- +goose StatementBegin
-- Images an administrator uploads for card overlay badges. ref is the
-- content address of the stored WebP ("<sha256 hex>.webp"); the bytes live in
-- the assets blob store under overlay-images/<ref>, and card overlay
-- documents (ui.card_overlays and the server default) refer to them by ref.
-- Uploading the same bytes again keeps one row and takes the new name.
CREATE TABLE public.overlay_images (
    ref        text        PRIMARY KEY,
    name       text        NOT NULL,
    width      integer     NOT NULL,
    height     integer     NOT NULL,
    bytes      integer     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.overlay_images;
-- +goose StatementEnd
