-- +goose Up
ALTER TABLE playback_v3_attempts
    ADD COLUMN control_state TEXT NOT NULL DEFAULT 'legacy',
    ADD COLUMN control_owner UUID,
    ADD COLUMN control_epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN control_lease_expires_at TIMESTAMPTZ,
    ADD CONSTRAINT playback_attempt_authority_state CHECK (
        (control_state = 'legacy' AND control_owner IS NULL AND control_epoch = 0 AND control_lease_expires_at IS NULL)
        OR (control_state IN ('preparing', 'active', 'terminal', 'stopped')
            AND control_owner IS NOT NULL AND control_epoch > 0 AND control_lease_expires_at IS NOT NULL)
    );

-- +goose Down
-- Authority/tombstone rows must not become replayable legacy sessions on rollback.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM playback_v3_attempts WHERE control_state <> 'legacy') THEN
        RAISE EXCEPTION 'Cannot remove playback authority while reserved attempts remain';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE playback_v3_attempts
    DROP CONSTRAINT playback_attempt_authority_state,
    DROP COLUMN control_state,
    DROP COLUMN control_owner,
    DROP COLUMN control_epoch,
    DROP COLUMN control_lease_expires_at;
