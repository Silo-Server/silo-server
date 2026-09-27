-- +goose Up
ALTER TABLE playback_v3_attempts
    ADD COLUMN control_route JSONB,
    ADD COLUMN control_grant_not_after TIMESTAMPTZ,
    ADD COLUMN control_drain_not_before TIMESTAMPTZ,
    DROP CONSTRAINT playback_attempt_authority_state,
    ADD CONSTRAINT playback_attempt_authority_state CHECK (
        (control_state = 'legacy' AND control_owner IS NULL AND control_epoch = 0 AND control_lease_expires_at IS NULL)
        OR (control_state IN ('preparing', 'active', 'terminal', 'stopped', 'draining')
            AND control_owner IS NOT NULL AND control_epoch > 0 AND control_lease_expires_at IS NOT NULL)
    ),
    ADD CONSTRAINT playback_attempt_grant_bounds CHECK (
        (control_route IS NULL OR (control_state <> 'legacy' AND jsonb_typeof(control_route) = 'object'))
        AND (control_grant_not_after IS NULL OR (control_route IS NOT NULL AND control_grant_not_after <= expires_at))
        AND (control_state <> 'draining' OR control_drain_not_before IS NOT NULL)
        AND (control_drain_not_before IS NULL OR control_grant_not_after IS NULL OR control_drain_not_before >= control_grant_not_after)
    );

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM playback_v3_attempts WHERE control_route IS NOT NULL OR control_state = 'draining') THEN
        RAISE EXCEPTION 'Cannot remove playback grants while bound routes remain';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE playback_v3_attempts
    DROP CONSTRAINT playback_attempt_grant_bounds,
    DROP CONSTRAINT playback_attempt_authority_state,
    ADD CONSTRAINT playback_attempt_authority_state CHECK (
        (control_state = 'legacy' AND control_owner IS NULL AND control_epoch = 0 AND control_lease_expires_at IS NULL)
        OR (control_state IN ('preparing', 'active', 'terminal', 'stopped')
            AND control_owner IS NOT NULL AND control_epoch > 0 AND control_lease_expires_at IS NOT NULL)
    ),
    DROP COLUMN control_route,
    DROP COLUMN control_grant_not_after,
    DROP COLUMN control_drain_not_before;
