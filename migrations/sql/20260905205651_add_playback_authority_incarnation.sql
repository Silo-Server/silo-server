-- +goose Up
ALTER TABLE playback_v3_attempts ADD COLUMN control_incarnation UUID;
UPDATE playback_v3_attempts SET control_incarnation = gen_random_uuid()
    WHERE control_state <> 'legacy';
ALTER TABLE playback_v3_attempts ADD CONSTRAINT playback_attempt_authority_incarnation CHECK (
    (control_state = 'legacy' AND control_incarnation IS NULL)
    OR (control_state <> 'legacy' AND control_incarnation IS NOT NULL)
);

-- +goose Down
-- Removing the incarnation would allow an old row-local epoch to match again.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM playback_v3_attempts WHERE control_state <> 'legacy') THEN
        RAISE EXCEPTION 'Cannot remove playback incarnation while reserved attempts remain';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE playback_v3_attempts
    DROP CONSTRAINT playback_attempt_authority_incarnation,
    DROP COLUMN control_incarnation;
