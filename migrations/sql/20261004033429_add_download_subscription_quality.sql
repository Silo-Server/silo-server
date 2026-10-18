-- The quality a series monitor downloads its episodes in. Monitors registered
-- only original files before; existing rows keep that through the default.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.download_subscriptions
    ADD COLUMN quality text NOT NULL DEFAULT 'original',
    ADD CONSTRAINT download_subscriptions_quality_check
        CHECK (quality IN ('original','20mbps','10mbps','5mbps','2mbps','1mbps'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.download_subscriptions
    DROP CONSTRAINT IF EXISTS download_subscriptions_quality_check,
    DROP COLUMN IF EXISTS quality;
-- +goose StatementEnd
