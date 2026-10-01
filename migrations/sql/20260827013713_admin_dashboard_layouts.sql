-- +goose Up
-- Per-admin-account admin dashboard widget layout. The server treats the blob
-- as opaque; the web client validates widget ids and spans when it loads it.
CREATE TABLE IF NOT EXISTS admin_dashboard_layouts (
    user_id    integer     PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    layout     jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS admin_dashboard_layouts;
