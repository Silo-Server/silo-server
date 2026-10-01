-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_nodes
    ADD COLUMN public_url text;

COMMENT ON COLUMN stream_nodes.public_url IS
    'Base URL streaming clients are given for this node, when it differs from '
    'url. url is the backend address: what the API server dials for health '
    'checks, capability fetches, and dispatch, and what a proxy dials to reach '
    'a transcode node — on a private network that should be a private address, '
    'which keeps co-located proxy/transcode traffic on the LAN instead of '
    'hairpinning through a public load balancer. public_url is only ever used '
    'to build the stream and download URLs handed to clients, so it is only '
    'meaningful on proxy nodes: clients never talk to transcode nodes. NULL '
    'means clients use url, which is every deployment registered before the '
    'column existed and every deployment with one flat network.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes
    DROP COLUMN IF EXISTS public_url;
-- +goose StatementEnd
