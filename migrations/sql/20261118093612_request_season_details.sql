-- +goose Up
-- Each requested season's TMDB first air date and episode count, kept when
-- the request is made. Router plugins match a season's episodes by air date
-- with them when their download service numbers the series' seasons
-- differently. Empty for whole-series requests and older requests, which
-- plugins match by season number as before.
ALTER TABLE media_requests ADD COLUMN season_details jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE media_requests DROP COLUMN season_details;
