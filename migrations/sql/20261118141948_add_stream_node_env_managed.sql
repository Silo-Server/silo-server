-- +goose Up
-- +goose StatementBegin
-- env_managed names the configuration fields the node itself declared from its
-- environment at its last start (public_url, group, max_jobs, ...). An
-- administrator update that would change one of them is refused while it is
-- listed. It describes who owns a field, not the field's value, so it is not
-- configuration: track_stream_node_configuration compares only the
-- configuration columns, and an update that changes env_managed alone keeps
-- admin_revision and leaves the pool generation where it is.
ALTER TABLE public.stream_nodes
    ADD COLUMN env_managed text[] NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.stream_nodes
    DROP COLUMN IF EXISTS env_managed;
-- +goose StatementEnd
