-- +goose Up
-- +goose StatementBegin
ALTER TABLE scan_runs
    ADD COLUMN claim_token TEXT NULL;

-- The first upgraded node takes this lock before checking for running work.
-- That makes the rollout fail closed: operators must let existing scans drain
-- before any upgraded worker can issue token-bearing claims.
LOCK TABLE scan_runs IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM scan_runs WHERE status = 'running') THEN
        RAISE EXCEPTION 'scan claim fencing migration requires all running scans to drain first';
    END IF;
END
$$;

COMMENT ON COLUMN scan_runs.claim_token IS
    'Opaque ownership token for fenced scan workers; NULL on legacy or unclaimed rows';

CREATE FUNCTION enforce_scan_run_claim_fencing()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status = 'running' AND NEW.status = 'accepted' THEN
        RAISE EXCEPTION 'in-place scan requeue is disabled';
    END IF;
    IF OLD.status = 'accepted' AND NEW.status = 'running'
       AND (NEW.claim_token IS NULL OR NEW.claim_token IS NOT DISTINCT FROM OLD.claim_token) THEN
        RAISE EXCEPTION 'scan claims require a fresh ownership token';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER scan_runs_claim_fencing
    BEFORE UPDATE OF status, claim_token ON scan_runs
    FOR EACH ROW
    EXECUTE FUNCTION enforce_scan_run_claim_fencing();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS scan_runs_claim_fencing ON scan_runs;
DROP FUNCTION IF EXISTS enforce_scan_run_claim_fencing();

ALTER TABLE scan_runs
    DROP COLUMN IF EXISTS claim_token;
-- +goose StatementEnd
