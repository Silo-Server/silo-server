-- +goose Up
-- host_api_scopes is the API key scopes the installed manifest asks for. The
-- manifest itself is not stored in a column, so the installer writes this
-- wherever it writes plugin_capabilities.
ALTER TABLE public.plugin_installations
    ADD COLUMN host_api_scopes text[] NOT NULL DEFAULT '{}';

-- What an administrator approved for one installation: the event access
-- classes it receives and the host API scopes its issued key carries.
-- api_key_id and api_key_ciphertext name that key; deleting the key (on the
-- API keys page, or with its owner's account) leaves the grant without one.
-- api_key_ciphertext is an enc:v1 envelope bound to the installation and key.
CREATE TABLE public.plugin_permission_grants (
    plugin_installation_id   bigint PRIMARY KEY REFERENCES public.plugin_installations(id) ON DELETE CASCADE,
    approved_event_access    text[] NOT NULL DEFAULT '{}',
    approved_host_api_scopes text[] NOT NULL DEFAULT '{}',
    approved_by_user_id      integer REFERENCES public.users(id) ON DELETE SET NULL,
    approved_at              timestamptz,
    api_key_id               bigint REFERENCES public.api_keys(id) ON DELETE SET NULL,
    api_key_ciphertext       text,
    updated_at               timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_plugin_permission_grants_api_key
    ON public.plugin_permission_grants (api_key_id)
    WHERE api_key_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS public.plugin_permission_grants;
ALTER TABLE public.plugin_installations DROP COLUMN IF EXISTS host_api_scopes;
