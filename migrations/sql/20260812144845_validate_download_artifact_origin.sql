-- +goose Up
ALTER TABLE public.download_artifacts
    VALIDATE CONSTRAINT download_artifacts_origin_check;

-- +goose Down
SELECT 1;
