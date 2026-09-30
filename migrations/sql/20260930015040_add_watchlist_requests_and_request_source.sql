-- +goose Up
-- +goose StatementBegin
-- Whether adding a title that is not in the library to a watchlist may also
-- request it. A profile can opt out on its own through the
-- requests.watchlist_auto_request setting; this is the server-wide switch.
ALTER TABLE public.request_settings
    ADD COLUMN IF NOT EXISTS watchlist_requests boolean NOT NULL DEFAULT true;

-- What created a request: the Request button ('direct') or a watchlist add
-- ('watchlist'). Removing the title from the watchlist cancels only a request
-- the watchlist created, and the admin queue labels those requests.
ALTER TABLE public.media_requests
    ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'direct'
        CONSTRAINT media_requests_source_check CHECK (source IN ('direct', 'watchlist'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.media_requests DROP COLUMN IF EXISTS source;
ALTER TABLE public.request_settings DROP COLUMN IF EXISTS watchlist_requests;
-- +goose StatementEnd
