-- +goose Up
-- Each claim keeps other servers from repeating its firing for its own task's
-- window, which for a long interval trigger exceeds any fixed retention. The
-- claim now records when it stops mattering, so pruning one task's claims
-- never cuts another task's window short. Existing claims keep the eight days
-- the previous fixed retention gave them.
ALTER TABLE task_firing_claims ADD COLUMN expires_at TIMESTAMPTZ;
UPDATE task_firing_claims SET expires_at = claimed_at + interval '8 days';
ALTER TABLE task_firing_claims ALTER COLUMN expires_at SET NOT NULL;
CREATE INDEX task_firing_claims_expires_idx ON task_firing_claims (expires_at);

-- +goose Down
DROP INDEX task_firing_claims_expires_idx;
ALTER TABLE task_firing_claims DROP COLUMN expires_at;
