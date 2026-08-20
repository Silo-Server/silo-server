-- +goose Up
-- +goose StatementBegin
ALTER TABLE playback_v3_attempts
    ALTER COLUMN session_id DROP NOT NULL,
    ADD COLUMN start_response JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD CONSTRAINT playback_v3_attempts_start_response_object
        CHECK (jsonb_typeof(start_response) = 'object');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM playback_v3_attempts WHERE session_id IS NULL;

ALTER TABLE playback_v3_attempts
    DROP CONSTRAINT IF EXISTS playback_v3_attempts_start_response_object,
    DROP COLUMN IF EXISTS start_response,
    ALTER COLUMN session_id SET NOT NULL;
-- +goose StatementEnd
