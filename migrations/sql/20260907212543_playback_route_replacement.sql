-- +goose Up
ALTER TABLE playback_v3_replans ADD COLUMN route_replacement jsonb;
ALTER TABLE playback_v3_replans ADD CONSTRAINT playback_route_replacement_bounded
 CHECK (route_replacement IS NULL OR (jsonb_typeof(route_replacement) = 'object' AND octet_length(route_replacement::text) <= 262144));
CREATE UNIQUE INDEX playback_one_pending_route_replacement ON playback_v3_replans(session_id)
 WHERE route_replacement->>'phase' IN ('staged', 'ready', 'retiring');

ALTER TABLE playback_v3_attempts ADD COLUMN control_retiring_replan text;
ALTER TABLE playback_v3_attempts DROP CONSTRAINT playback_attempt_grant_bounds;
ALTER TABLE playback_v3_attempts ADD CONSTRAINT playback_attempt_grant_bounds CHECK (
 (control_route IS NULL OR (control_state <> 'legacy' AND jsonb_typeof(control_route) = 'object'))
 AND (control_retiring_replan IS NULL OR (control_state IN ('active','draining','stopped') AND control_route IS NULL))
 AND (control_grant_not_after IS NULL OR ((control_route IS NOT NULL OR control_retiring_replan IS NOT NULL) AND control_grant_not_after <= expires_at))
 AND (control_state <> 'draining' OR control_drain_not_before IS NOT NULL)
 AND (control_drain_not_before IS NULL OR control_grant_not_after IS NULL OR control_drain_not_before >= control_grant_not_after)
);

-- +goose Down
-- Refuse downgrade rather than erase retained cleanup identities.
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM playback_v3_replans WHERE route_replacement IS NOT NULL) THEN
  RAISE EXCEPTION 'Cannot remove retained playback route replacement state';
 END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE playback_v3_attempts DROP CONSTRAINT playback_attempt_grant_bounds;
ALTER TABLE playback_v3_attempts ADD CONSTRAINT playback_attempt_grant_bounds CHECK (
 (control_route IS NULL OR (control_state <> 'legacy' AND jsonb_typeof(control_route) = 'object'))
 AND (control_grant_not_after IS NULL OR (control_route IS NOT NULL AND control_grant_not_after <= expires_at))
 AND (control_state <> 'draining' OR control_drain_not_before IS NOT NULL)
 AND (control_drain_not_before IS NULL OR control_grant_not_after IS NULL OR control_drain_not_before >= control_grant_not_after)
);
ALTER TABLE playback_v3_attempts DROP COLUMN control_retiring_replan;

DROP INDEX playback_one_pending_route_replacement;
ALTER TABLE playback_v3_replans DROP CONSTRAINT playback_route_replacement_bounded;
ALTER TABLE playback_v3_replans DROP COLUMN route_replacement;
