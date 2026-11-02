-- +goose Up
-- What the backup subsystem is doing, for every API server to read. Only the
-- holder of the cluster-wide backup lock (internal/backup) writes it: the
-- active operation while it runs, and the outcome of the latest backup run.
-- active_token names the operation; its progress and end writes apply only
-- while the row still carries that token. active_lock_pid and
-- active_lock_backend_start name the session holding the lock (each lock
-- session holds it once and is then closed), so a reader can tell the
-- operation actually holding it from a row left by a holder that died. The next holder records such a row
-- as an interrupted run before publishing its own. One row; backups leave
-- its data out of every dump.
CREATE TABLE IF NOT EXISTS backup_status (
    singleton        boolean     PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    active_operation text        NOT NULL DEFAULT '' CHECK (active_operation IN ('', 'backup', 'delete', 'restore')),
    active_backup_id text        NOT NULL DEFAULT '',
    active_kind      text        NOT NULL DEFAULT '',
    active_token     text        NOT NULL DEFAULT '',
    active_lock_pid  integer     NOT NULL DEFAULT 0,
    active_lock_backend_start timestamptz,
    active_started_at timestamptz,
    active_progress  real        NOT NULL DEFAULT 0,
    active_message   text        NOT NULL DEFAULT '',
    last_run_id      text        NOT NULL DEFAULT '',
    last_run_kind    text        NOT NULL DEFAULT '',
    last_run_status  text        NOT NULL DEFAULT '',
    last_run_started_at  timestamptz,
    last_run_finished_at timestamptz,
    last_run_error   text        NOT NULL DEFAULT '',
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS backup_status;
