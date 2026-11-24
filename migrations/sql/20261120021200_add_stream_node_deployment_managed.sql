-- +goose Up
-- deployment_managed marks a transcode node that the API servers' deployment
-- configuration (SILO_TRANSCODE_NODES) lists: they created its row or adopted
-- it, and they own its key. Its key cannot be issued and its row cannot be
-- deleted from the admin API while it is set; an API server that starts with a
-- configuration that no longer lists the node clears it. It describes who owns
-- the node, not its configuration, so track_stream_node_configuration, which
-- compares an explicit column list, ignores it like node_key_ciphertext.
ALTER TABLE public.stream_nodes
    ADD COLUMN deployment_managed boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE public.stream_nodes
    DROP COLUMN IF EXISTS deployment_managed;
