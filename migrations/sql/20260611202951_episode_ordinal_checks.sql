-- +goose Up
-- +goose StatementBegin
-- Enforce the episode-key invariants (episode_key.go: EpisodeKey /
-- ValidEpisodeOrdinals) at the database layer too, so direct SQL or future
-- backfill paths cannot insert rows that break the collision-free ordering
-- contract the fanout and suppression logic depend on. Bounds mirror the Go
-- constants: season <= 2146 keeps season * 1,000,000 + episode inside int4.
ALTER TABLE public.episode_availability
    ADD CONSTRAINT episode_availability_ordinals_check CHECK (
        season_number >= 0
        AND season_number <= 2146
        AND episode_number >= 0
        AND episode_number < 1000000
        AND episode_key = season_number * 1000000 + episode_number
    );

ALTER TABLE public.release_events
    ADD CONSTRAINT release_events_ordinals_check CHECK (
        season_number >= 0
        AND season_number <= 2146
        AND episode_number >= 0
        AND episode_number < 1000000
        AND episode_key = season_number * 1000000 + episode_number
    );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.episode_availability
    DROP CONSTRAINT IF EXISTS episode_availability_ordinals_check;
ALTER TABLE public.release_events
    DROP CONSTRAINT IF EXISTS release_events_ordinals_check;
-- +goose StatementEnd
