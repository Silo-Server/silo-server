-- +goose Up
-- The distributed playback authority protocol (owner leases, activation
-- phases, serving grants, route replacement, transfer permits, progress sinks,
-- source admission) is retired. Progress and stop sequencing now live on the
-- attempt row itself as a compare-and-set over last_sequence / stopped_at.

DROP TABLE IF EXISTS playback_automatic_admission_intents;
DROP TABLE IF EXISTS playback_auxiliary_transfer_permits;
DROP TABLE IF EXISTS playback_first_admissions;
DROP TABLE IF EXISTS playback_output_transfer_permits;
DROP TABLE IF EXISTS playback_progress_sinks;
DROP TABLE IF EXISTS playback_source_markers;
DROP TABLE IF EXISTS playback_source_registrations;

-- playback_v3_replans: route replacement (20260907212543).
DROP INDEX IF EXISTS playback_one_pending_route_replacement;
ALTER TABLE playback_v3_replans
    DROP CONSTRAINT IF EXISTS playback_route_replacement_bounded,
    DROP COLUMN IF EXISTS route_replacement;

-- playback_v3_attempts: every control_* column and the constraints/indexes
-- over them (20260905204220, 20260905205651, 20260905210553, 20260905212048,
-- 20260905214502, 20260905234207, 20260907204648, 20260907212543).
DROP INDEX IF EXISTS playback_v3_attempts_control_incarnation_idx;
ALTER TABLE playback_v3_attempts
    DROP CONSTRAINT IF EXISTS playback_attempt_authority_state,
    DROP CONSTRAINT IF EXISTS playback_attempt_authority_incarnation,
    DROP CONSTRAINT IF EXISTS playback_attempt_grant_bounds,
    DROP CONSTRAINT IF EXISTS playback_attempt_recipe_locator_binding,
    DROP COLUMN IF EXISTS control_state,
    DROP COLUMN IF EXISTS control_owner,
    DROP COLUMN IF EXISTS control_epoch,
    DROP COLUMN IF EXISTS control_lease_expires_at,
    DROP COLUMN IF EXISTS control_incarnation,
    DROP COLUMN IF EXISTS control_route,
    DROP COLUMN IF EXISTS control_grant_not_after,
    DROP COLUMN IF EXISTS control_drain_not_before,
    DROP COLUMN IF EXISTS control_recipe_locator,
    DROP COLUMN IF EXISTS control_activation,
    DROP COLUMN IF EXISTS control_reservation_admission_id,
    DROP COLUMN IF EXISTS control_retiring_replan;

-- Durable per-attempt progress/stop sequencing. last_sample and stop_receipt
-- are small JSON documents: {sequence, position, is_paused} and
-- {stop_id, accepted, history_id}.
ALTER TABLE playback_v3_attempts
    ADD COLUMN last_sequence BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN last_sample JSONB,
    ADD COLUMN stopped_at TIMESTAMPTZ,
    ADD COLUMN stop_id UUID,
    ADD COLUMN stop_receipt JSONB;

-- +goose Down
ALTER TABLE playback_v3_attempts
    DROP COLUMN stop_receipt,
    DROP COLUMN stop_id,
    DROP COLUMN stopped_at,
    DROP COLUMN last_sample,
    DROP COLUMN last_sequence;

-- Restored in their final pre-removal shape (the state after 20260907212543).
ALTER TABLE playback_v3_attempts
    ADD COLUMN control_state TEXT NOT NULL DEFAULT 'legacy',
    ADD COLUMN control_owner UUID,
    ADD COLUMN control_epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN control_lease_expires_at TIMESTAMPTZ,
    ADD COLUMN control_incarnation UUID,
    ADD COLUMN control_route JSONB,
    ADD COLUMN control_grant_not_after TIMESTAMPTZ,
    ADD COLUMN control_drain_not_before TIMESTAMPTZ,
    ADD COLUMN control_recipe_locator JSONB,
    ADD COLUMN control_activation JSONB
        CHECK (control_activation IS NULL OR jsonb_typeof(control_activation) = 'object'),
    ADD COLUMN control_reservation_admission_id UUID,
    ADD COLUMN control_retiring_replan text,
    ADD CONSTRAINT playback_attempt_authority_state CHECK (
        (control_state = 'legacy' AND control_owner IS NULL AND control_epoch = 0 AND control_lease_expires_at IS NULL)
        OR (control_state IN ('preparing', 'active', 'terminal', 'stopped', 'draining')
            AND control_owner IS NOT NULL AND control_epoch > 0 AND control_lease_expires_at IS NOT NULL)
    ),
    ADD CONSTRAINT playback_attempt_authority_incarnation CHECK (
        (control_state = 'legacy' AND control_incarnation IS NULL)
        OR (control_state <> 'legacy' AND control_incarnation IS NOT NULL)
    ),
    ADD CONSTRAINT playback_attempt_grant_bounds CHECK (
        (control_route IS NULL OR (control_state <> 'legacy' AND jsonb_typeof(control_route) = 'object'))
        AND (control_retiring_replan IS NULL OR (control_state IN ('active','draining','stopped') AND control_route IS NULL))
        AND (control_grant_not_after IS NULL OR ((control_route IS NOT NULL OR control_retiring_replan IS NOT NULL) AND control_grant_not_after <= expires_at))
        AND (control_state <> 'draining' OR control_drain_not_before IS NOT NULL)
        AND (control_drain_not_before IS NULL OR control_grant_not_after IS NULL OR control_drain_not_before >= control_grant_not_after)
    ),
    ADD CONSTRAINT playback_attempt_recipe_locator_binding CHECK (
        control_recipe_locator IS NULL OR (
            control_route IS NOT NULL
            AND jsonb_typeof(control_recipe_locator) = 'object'
            AND control_recipe_locator ? 'executor'
            AND control_route ? 'executor'
            AND control_recipe_locator->'executor' = control_route->'executor'
        )
    );
CREATE UNIQUE INDEX playback_v3_attempts_control_incarnation_idx
    ON playback_v3_attempts (control_incarnation)
    WHERE control_incarnation IS NOT NULL;

ALTER TABLE playback_v3_replans ADD COLUMN route_replacement jsonb;
ALTER TABLE playback_v3_replans ADD CONSTRAINT playback_route_replacement_bounded
 CHECK (route_replacement IS NULL OR (jsonb_typeof(route_replacement) = 'object' AND octet_length(route_replacement::text) <= 262144));
CREATE UNIQUE INDEX playback_one_pending_route_replacement ON playback_v3_replans(session_id)
 WHERE route_replacement->>'phase' IN ('staged', 'ready', 'retiring');

CREATE TABLE public.playback_progress_sinks (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    session_id text NOT NULL,
    media_item_id text NOT NULL,
    attempt_id text NOT NULL,
    incarnation text NOT NULL,
    owner_id text NOT NULL,
    epoch bigint NOT NULL CHECK (epoch > 0),
    state text NOT NULL CHECK (state IN ('active', 'stopped')),
    last_sequence bigint NOT NULL CHECK (last_sequence >= 0),
    document jsonb NOT NULL CHECK (jsonb_typeof(document) = 'object' AND octet_length(document::text) <= 262144),
    PRIMARY KEY (user_id, profile_id, session_id)
);

CREATE TABLE public.playback_source_markers (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    source_id UUID NOT NULL,
    selection_generation BIGINT NOT NULL CHECK (selection_generation > 0),
    gate TEXT NOT NULL DEFAULT 'quarantined' CHECK (gate IN ('writable', 'quarantined', 'sealed'))
);

CREATE TABLE playback_source_registrations (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    backend TEXT NOT NULL CHECK (backend IN ('postgres', 'sqlite')),
    source_id UUID NOT NULL,
    selection_generation BIGINT NOT NULL CHECK (selection_generation > 0),
    admission_id UUID NOT NULL,
    admission_state TEXT NOT NULL DEFAULT 'blocked' CHECK (admission_state IN ('admitting', 'blocked', 'retiring'))
);

CREATE TABLE playback_first_admissions (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 installation_id UUID NOT NULL,
 intent_id UUID NOT NULL UNIQUE,
 source_id UUID NOT NULL UNIQUE,
 expected_username TEXT NOT NULL,
 admitted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE playback_output_transfer_permits (
    permit_id UUID PRIMARY KEY,
    playback_attempt_id TEXT NOT NULL REFERENCES playback_v3_attempts(playback_attempt_id) ON DELETE CASCADE,
    incarnation UUID NOT NULL,
    owner_id UUID NOT NULL,
    epoch BIGINT NOT NULL CHECK (epoch > 0),
    session_id UUID NOT NULL,
    plan_id TEXT NOT NULL,
    transport_id TEXT NOT NULL,
    executor JSONB NOT NULL,
    execution_node_id BIGINT NOT NULL CHECK (execution_node_id >= 0),
    egress_node_id BIGINT NOT NULL CHECK (egress_node_id >= 0)
);
CREATE INDEX playback_output_transfer_attempt ON playback_output_transfer_permits(playback_attempt_id);

CREATE TABLE playback_auxiliary_transfer_permits (
    permit_id UUID PRIMARY KEY,
    playback_attempt_id TEXT NOT NULL REFERENCES playback_v3_attempts(playback_attempt_id) ON DELETE CASCADE,
    incarnation UUID NOT NULL,
    owner_id UUID NOT NULL,
    epoch BIGINT NOT NULL CHECK (epoch > 0),
    session_id UUID NOT NULL,
    plan_id TEXT NOT NULL,
    transport_id TEXT NOT NULL,
    executor JSONB NOT NULL,
    egress_node_id BIGINT NOT NULL CHECK (egress_node_id > 0)
);
CREATE INDEX playback_auxiliary_transfer_attempt ON playback_auxiliary_transfer_permits(playback_attempt_id);

CREATE TABLE playback_automatic_admission_intents (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 installation_id UUID NOT NULL,
 expected_username TEXT NOT NULL,
 source_id UUID NOT NULL UNIQUE,
 intent_id UUID NOT NULL UNIQUE
);
