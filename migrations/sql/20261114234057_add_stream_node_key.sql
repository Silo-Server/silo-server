-- +goose Up
-- A transcode node's own request-signing key, encrypted with the data cipher
-- under secret.RowAAD('stream_nodes', 'node_key_ciphertext', <id>). It is a
-- credential, not configuration: track_stream_node_configuration compares an
-- explicit column list that leaves it out, so issuing or rotating a key moves
-- neither admin_revision nor the pool generation.
ALTER TABLE public.stream_nodes
    ADD COLUMN node_key_ciphertext text;

COMMENT ON COLUMN public.stream_nodes.node_key_ciphertext IS
    'Encrypted node key of a transcode node. NULL means no key has been issued.';

-- +goose Down
ALTER TABLE public.stream_nodes
    DROP COLUMN IF EXISTS node_key_ciphertext;
