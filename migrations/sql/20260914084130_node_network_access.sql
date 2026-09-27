-- +goose Up
-- +goose StatementBegin
-- Last network access provider status a proxy node reported on its health
-- check, keyed by provider slug:
--   {"tailscale": {"state": "connected", "origin": "https://proxy-1.tail1234.ts.net",
--                  "hostname": "proxy-1.tail1234.ts.net", "updated_at": "..."}}
-- Rewritten by every health sweep from the node's own report; an empty object
-- means the node reports no providers (or predates the field). Stream URLs
-- handed to a client that arrived through a provider are built on the matching
-- connected origin, never on the node's LAN/public URL.
ALTER TABLE stream_nodes ADD COLUMN network_access JSONB NOT NULL DEFAULT '{}'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_nodes DROP COLUMN IF EXISTS network_access;
-- +goose StatementEnd
