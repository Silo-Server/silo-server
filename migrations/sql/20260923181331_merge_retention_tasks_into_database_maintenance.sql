-- +goose Up
-- These retention tasks now run as steps of database_maintenance, which seeds
-- its own default schedule. Drop their saved schedules so they leave no
-- orphaned rows. Their execution history stays; task history retention prunes
-- it down to the newest row per key.
DELETE FROM task_triggers
WHERE task_key IN (
    'cleanup_activity_log',
    'cleanup_policy_decision_log',
    'cleanup_task_history',
    'cleanup_auth_sessions',
    'cleanup_catalog_search_index_events',
    'notifications_retention'
);
DELETE FROM task_schedules
WHERE task_key IN (
    'cleanup_activity_log',
    'cleanup_policy_decision_log',
    'cleanup_task_history',
    'cleanup_auth_sessions',
    'cleanup_catalog_search_index_events',
    'notifications_retention'
);

-- +goose Down
-- The separate tasks reseed their defaults on the next start. Remove the
-- combined task's schedule so it does not linger unused.
DELETE FROM task_triggers WHERE task_key = 'database_maintenance';
DELETE FROM task_schedules WHERE task_key = 'database_maintenance';
