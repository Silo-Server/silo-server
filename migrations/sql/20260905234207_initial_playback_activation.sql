-- +goose Up
CREATE TABLE playback_source_registrations (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    backend TEXT NOT NULL CHECK (backend IN ('postgres', 'sqlite')),
    source_id UUID NOT NULL,
    selection_generation BIGINT NOT NULL CHECK (selection_generation > 0),
    admission_id UUID NOT NULL,
    admission_state TEXT NOT NULL DEFAULT 'blocked' CHECK (admission_state IN ('admitting', 'blocked', 'retiring'))
);
-- No registration is created or admitted by this migration.
ALTER TABLE playback_v3_attempts ADD COLUMN control_activation JSONB
    CHECK (control_activation IS NULL OR jsonb_typeof(control_activation) = 'object');

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM playback_v3_attempts WHERE control_activation IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove retained playback activation bindings';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE playback_v3_attempts DROP COLUMN control_activation;
DROP TABLE playback_source_registrations;
