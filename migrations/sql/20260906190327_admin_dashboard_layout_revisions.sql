-- +goose Up
-- Retain a generation after reset so a stale missing-layout validator cannot
-- become current again. The account FK removes this tombstone on account deletion.
CREATE TABLE admin_dashboard_layout_revisions (
    user_id integer PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    revision uuid NOT NULL DEFAULT gen_random_uuid()
);
INSERT INTO admin_dashboard_layout_revisions (user_id)
SELECT user_id FROM admin_dashboard_layouts;

-- +goose Down
DROP TABLE admin_dashboard_layout_revisions;
