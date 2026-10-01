-- +goose Up
-- Retains the exact first-admission decision. No accounts are admitted here.
CREATE TABLE playback_first_admissions (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 installation_id UUID NOT NULL,
 intent_id UUID NOT NULL UNIQUE,
 source_id UUID NOT NULL UNIQUE,
 expected_username TEXT NOT NULL,
 admitted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM playback_first_admissions) THEN
  RAISE EXCEPTION 'cannot remove retained playback first-admission decisions';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE playback_first_admissions;
