-- +goose Up
-- Proposals survive uncertain apply commits and concurrent API requests.
CREATE TABLE playback_automatic_admission_intents (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 installation_id UUID NOT NULL,
 expected_username TEXT NOT NULL,
 source_id UUID NOT NULL UNIQUE,
 intent_id UUID NOT NULL UNIQUE
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM playback_automatic_admission_intents) THEN
  RAISE EXCEPTION 'cannot remove retained automatic first-admission intents';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE playback_automatic_admission_intents;
