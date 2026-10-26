-- +goose Up
-- What routing rules can match on (genres, keywords, original language,
-- origin countries, year, networks, studios, anime), captured from TMDB when
-- the request is created. Rules then evaluate the same way at approval, on
-- another server, or after TMDB changes. '{}' marks a request from before
-- capture; routing fetches its facts when it is submitted.
ALTER TABLE media_requests ADD COLUMN routing_facts jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE media_requests DROP COLUMN routing_facts;
