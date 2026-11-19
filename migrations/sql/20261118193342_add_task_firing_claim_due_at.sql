-- +goose Up
-- A claim also records when its firing was due. claimed_at is when the row
-- was written, which for a server that reached the claim late says nothing
-- about which firing it served: a second late server, due seconds apart
-- across a slot boundary, compared its own due time with that late
-- claimed_at and ran the firing again. Claims from before this column keep
-- claimed_at as their due time.
ALTER TABLE task_firing_claims ADD COLUMN due_at TIMESTAMPTZ;
UPDATE task_firing_claims SET due_at = claimed_at;
ALTER TABLE task_firing_claims ALTER COLUMN due_at SET NOT NULL;

-- +goose Down
ALTER TABLE task_firing_claims DROP COLUMN due_at;
