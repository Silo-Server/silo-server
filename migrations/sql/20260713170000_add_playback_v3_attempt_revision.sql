-- +goose Up
ALTER TABLE playback_v3_attempts
    ADD COLUMN current_replan_request_id TEXT NOT NULL DEFAULT '';

ALTER TABLE playback_v3_replans
    ADD COLUMN base_replan_request_id TEXT NOT NULL DEFAULT '';

WITH latest_completed AS (
    SELECT DISTINCT ON (session_id)
        session_id,
        replan_request_id
    FROM playback_v3_replans
    WHERE state = 'completed'
    ORDER BY session_id, updated_at DESC, created_at DESC, replan_request_id DESC
)
UPDATE playback_v3_attempts AS attempts
SET current_replan_request_id = latest_completed.replan_request_id
FROM latest_completed
WHERE attempts.session_id = latest_completed.session_id;

UPDATE playback_v3_replans AS candidate
SET base_replan_request_id = COALESCE((
    SELECT previous.replan_request_id
    FROM playback_v3_replans AS previous
    WHERE previous.session_id = candidate.session_id
      AND previous.state = 'completed'
      AND previous.updated_at <= candidate.created_at
    ORDER BY previous.updated_at DESC, previous.created_at DESC, previous.replan_request_id DESC
    LIMIT 1
), '');

-- Keep the revision correct during rolling deploys and rollbacks. Older V3
-- binaries still complete playback_v3_replans in the same transaction, so
-- this trigger advances the attempt marker even when their attempt UPDATE
-- does not know about the new column.
-- +goose StatementBegin
CREATE FUNCTION sync_playback_v3_current_replan_request_id()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.state = 'completed' THEN
        -- New binaries already write the marker in the same transaction; the
        -- IS DISTINCT FROM guard skips a redundant second row version there
        -- while still advancing the marker for older binaries.
        UPDATE playback_v3_attempts
        SET current_replan_request_id = NEW.replan_request_id
        WHERE session_id = NEW.session_id
          AND current_replan_request_id IS DISTINCT FROM NEW.replan_request_id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER playback_v3_replans_sync_attempt_revision
AFTER UPDATE OF state, response ON playback_v3_replans
FOR EACH ROW
EXECUTE FUNCTION sync_playback_v3_current_replan_request_id();

-- +goose StatementBegin
CREATE FUNCTION seed_playback_v3_replan_base_revision()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.base_replan_request_id = '' THEN
        SELECT current_replan_request_id
        INTO NEW.base_replan_request_id
        FROM playback_v3_attempts
        WHERE session_id = NEW.session_id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER playback_v3_replans_seed_base_revision
BEFORE INSERT ON playback_v3_replans
FOR EACH ROW
EXECUTE FUNCTION seed_playback_v3_replan_base_revision();

-- +goose Down
DROP TRIGGER IF EXISTS playback_v3_replans_seed_base_revision ON playback_v3_replans;
DROP FUNCTION IF EXISTS seed_playback_v3_replan_base_revision();
DROP TRIGGER IF EXISTS playback_v3_replans_sync_attempt_revision ON playback_v3_replans;
DROP FUNCTION IF EXISTS sync_playback_v3_current_replan_request_id();

ALTER TABLE playback_v3_replans
    DROP COLUMN IF EXISTS base_replan_request_id;

ALTER TABLE playback_v3_attempts
    DROP COLUMN IF EXISTS current_replan_request_id;
