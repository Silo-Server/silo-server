-- +goose Up
-- Readers publish classification snapshots for at most two seconds. Every
-- actual protocol mutation drains them for three seconds while excluding new
-- readers, so a lookup admitted after commit cannot use a stale legacy result.
-- Keys 1397312591/1464816203 encode SILO/WORK; keep nodepool's constants aligned.
LOCK TABLE stream_nodes, worker_http_nodes IN SHARE ROW EXCLUSIVE MODE;

-- Raw URLs are retained because origin canonicalization belongs to Go. No
-- foreign key: deleting an enrolled node must never restore legacy transport.
CREATE TABLE worker_http_origin_history (raw_url TEXT NOT NULL);
INSERT INTO worker_http_origin_history(raw_url)
SELECT DISTINCT n.url FROM stream_nodes n JOIN worker_http_nodes w ON w.node_id=n.id;

-- +goose StatementBegin
CREATE FUNCTION lock_worker_registry_configuration() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(1397312591, 1464816203);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION drain_worker_registry_snapshots() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    transaction_id TEXT := pg_current_xact_id()::TEXT;
BEGIN
    IF current_setting('silo.worker_registry_drained_xid', TRUE) IS DISTINCT FROM transaction_id THEN
        -- Never condition this on a reader flag: an acknowledged flag can be
        -- lost during asynchronous database failover while a snapshot lives on.
        PERFORM pg_sleep(3);
        -- Set only after the drain succeeds; cancellation must not bless a
        -- partially drained transaction, including across savepoint rollback.
        PERFORM set_config('silo.worker_registry_drained_xid', transaction_id, TRUE);
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION retain_worker_registry_origin(origin_url TEXT) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO worker_http_origin_history(raw_url)
    SELECT origin_url WHERE NOT EXISTS (
        SELECT 1 FROM worker_http_origin_history WHERE raw_url=origin_url
    );
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION fence_worker_registry_membership() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    origin_url TEXT;
BEGIN
    IF TG_OP = 'UPDATE' AND ROW(NEW.node_id, NEW.public_key, NEW.allow_http)
        IS NOT DISTINCT FROM ROW(OLD.node_id, OLD.public_key, OLD.allow_http) THEN
        RETURN NEW;
    END IF;
    PERFORM drain_worker_registry_snapshots();
    IF TG_OP <> 'INSERT' THEN
        SELECT url INTO origin_url FROM stream_nodes WHERE id=OLD.node_id;
        IF origin_url IS NOT NULL THEN PERFORM retain_worker_registry_origin(origin_url); END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        SELECT url INTO origin_url FROM stream_nodes WHERE id=NEW.node_id;
        IF origin_url IS NOT NULL THEN PERFORM retain_worker_registry_origin(origin_url); END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION fence_stream_node_worker_origin() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND ROW(NEW.url, NEW.type) IS NOT DISTINCT FROM ROW(OLD.url, OLD.type) THEN
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM worker_http_nodes WHERE node_id=OLD.id) THEN
        PERFORM drain_worker_registry_snapshots();
        PERFORM retain_worker_registry_origin(OLD.url);
        IF TG_OP = 'UPDATE' THEN PERFORM retain_worker_registry_origin(NEW.url); END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION fence_worker_registry_history_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM drain_worker_registry_snapshots();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION reject_worker_registry_history_removal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'worker origin history is append-only' USING ERRCODE='42501';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION reject_worker_registry_truncate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'worker registry tables require row-by-row deletion' USING ERRCODE='42501';
END;
$$;
-- +goose StatementEnd

-- Acquire the global lock before row locks, including the existing configuration
-- revision trigger's generation lock. No-op and health writes do not drain.
CREATE TRIGGER stream_node_classification_lock
BEFORE UPDATE OF url, type OR DELETE ON stream_nodes
FOR EACH STATEMENT EXECUTE FUNCTION lock_worker_registry_configuration();
CREATE TRIGGER stream_node_classification_fence
BEFORE UPDATE OF url, type OR DELETE ON stream_nodes
FOR EACH ROW EXECUTE FUNCTION fence_stream_node_worker_origin();
CREATE TRIGGER worker_http_node_classification_lock
BEFORE INSERT OR UPDATE OR DELETE ON worker_http_nodes
FOR EACH STATEMENT EXECUTE FUNCTION lock_worker_registry_configuration();
CREATE TRIGGER worker_http_node_classification_fence
BEFORE INSERT OR UPDATE OR DELETE ON worker_http_nodes
FOR EACH ROW EXECUTE FUNCTION fence_worker_registry_membership();
CREATE TRIGGER worker_http_origin_history_lock
BEFORE INSERT ON worker_http_origin_history
FOR EACH STATEMENT EXECUTE FUNCTION lock_worker_registry_configuration();
CREATE TRIGGER worker_http_origin_history_insert_fence
BEFORE INSERT ON worker_http_origin_history
FOR EACH ROW EXECUTE FUNCTION fence_worker_registry_history_insert();
CREATE TRIGGER worker_http_origin_history_append_only
BEFORE UPDATE OR DELETE OR TRUNCATE ON worker_http_origin_history
FOR EACH STATEMENT EXECUTE FUNCTION reject_worker_registry_history_removal();
CREATE TRIGGER stream_node_worker_registry_no_truncate
BEFORE TRUNCATE ON stream_nodes
FOR EACH STATEMENT EXECUTE FUNCTION reject_worker_registry_truncate();
CREATE TRIGGER worker_http_node_no_truncate
BEFORE TRUNCATE ON worker_http_nodes
FOR EACH STATEMENT EXECUTE FUNCTION reject_worker_registry_truncate();

-- +goose Down
-- Drain snapshots before removing the triggers which make their TTL safe.
-- DML takes a table lock before its statement trigger waits for the fence.
-- Never wait for those table locks while retaining the fence, or for the fence
-- while retaining tables a snapshot reader still needs. Each failed attempt
-- rolls both lock types back in its subtransaction before retrying.
-- Keep authentication SELECTs available during the drain, then promote every
-- table with NOWAIT before DDL. A failed promotion also rolls back the drain's
-- transaction-local marker: readers admitted between attempts need a new drain.
-- +goose StatementBegin
DO $$
DECLARE
    deadline TIMESTAMPTZ := clock_timestamp() + INTERVAL '30 seconds';
BEGIN
    LOOP
        BEGIN
            IF NOT pg_try_advisory_xact_lock(1397312591, 1464816203) THEN
                RAISE lock_not_available;
            END IF;
            LOCK TABLE stream_nodes, worker_http_nodes, worker_http_origin_history
                IN SHARE ROW EXCLUSIVE MODE NOWAIT;
            PERFORM drain_worker_registry_snapshots();
            LOCK TABLE stream_nodes, worker_http_nodes, worker_http_origin_history
                IN ACCESS EXCLUSIVE MODE NOWAIT;
            EXIT;
        EXCEPTION WHEN lock_not_available THEN
            IF clock_timestamp() >= deadline THEN
                RAISE EXCEPTION 'worker registry rollback could not acquire configuration locks'
                    USING ERRCODE='55P03';
            END IF;
        END;
        PERFORM pg_sleep(0.05);
    END LOOP;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER worker_http_node_no_truncate ON worker_http_nodes;
DROP TRIGGER stream_node_worker_registry_no_truncate ON stream_nodes;
DROP TRIGGER worker_http_origin_history_append_only ON worker_http_origin_history;
DROP TRIGGER worker_http_origin_history_insert_fence ON worker_http_origin_history;
DROP TRIGGER worker_http_origin_history_lock ON worker_http_origin_history;
DROP TRIGGER worker_http_node_classification_fence ON worker_http_nodes;
DROP TRIGGER worker_http_node_classification_lock ON worker_http_nodes;
DROP TRIGGER stream_node_classification_fence ON stream_nodes;
DROP TRIGGER stream_node_classification_lock ON stream_nodes;
DROP FUNCTION reject_worker_registry_truncate();
DROP FUNCTION reject_worker_registry_history_removal();
DROP FUNCTION fence_worker_registry_history_insert();
DROP FUNCTION fence_stream_node_worker_origin();
DROP FUNCTION fence_worker_registry_membership();
DROP FUNCTION retain_worker_registry_origin(TEXT);
DROP FUNCTION drain_worker_registry_snapshots();
DROP FUNCTION lock_worker_registry_configuration();
DROP TABLE worker_http_origin_history;
