-- +goose Up
ALTER TABLE watch_together_rooms ADD COLUMN runtime jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE watch_together_rooms DROP COLUMN runtime;
