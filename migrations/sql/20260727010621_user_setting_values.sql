-- Canonical storage for the cross-platform user settings contract.
--
-- user_setting_values replaces the string-valued preference surfaces with one
-- typed table: the manifest in contracts/settings/v1 remains the schema, and
-- this table stores validated JSON plus the scope identity the value hangs off.
-- See docs/superpowers/specs/2026-07-10-cross-platform-user-settings-contract-design.md
-- ("Canonical storage").
--
-- Delete behavior is application-enforced. The two cascades below are the only
-- ones this schema can inherit — user ownership, and composite profile
-- ownership, which user_device_settings already carries. Library, series and
-- device identity columns reference nothing (libraries and series live in the
-- shared catalog, devices in user_devices, and the per-user SQLite store
-- declares no foreign keys at all), so the owning delete paths remove those
-- rows and the userstore conformance suite holds both backends to it.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.user_setting_values (
    id          bigserial PRIMARY KEY,
    user_id     integer NOT NULL,
    key         text NOT NULL,
    scope       text NOT NULL,
    profile_id  text,
    device_id   text,
    library_id  integer,
    series_id   text,
    value       jsonb NOT NULL,
    revision    bigint NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_setting_values_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    -- MATCH SIMPLE: account-scope rows carry a NULL profile_id and are exempt.
    CONSTRAINT user_setting_values_profile_fkey
        FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE,
    CONSTRAINT user_setting_values_scope_check
        CHECK (scope IN ('account', 'profile', 'profile_device', 'profile_library', 'profile_series')),
    CONSTRAINT user_setting_values_identity_check CHECK (
      (scope = 'account' AND profile_id IS NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_device' AND profile_id IS NOT NULL AND device_id IS NOT NULL AND library_id IS NULL AND series_id IS NULL) OR
      (scope = 'profile_library' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NOT NULL AND series_id IS NULL) OR
      (scope = 'profile_series' AND profile_id IS NOT NULL AND device_id IS NULL AND library_id IS NULL AND series_id IS NOT NULL)
    )
);

-- One explicit value per identity. These exist for correctness, not for reads.
CREATE UNIQUE INDEX user_setting_values_account_uq
  ON public.user_setting_values (user_id, key) WHERE scope = 'account';
CREATE UNIQUE INDEX user_setting_values_profile_uq
  ON public.user_setting_values (user_id, profile_id, key) WHERE scope = 'profile';
CREATE UNIQUE INDEX user_setting_values_profile_device_uq
  ON public.user_setting_values (user_id, profile_id, device_id, key) WHERE scope = 'profile_device';
CREATE UNIQUE INDEX user_setting_values_profile_library_uq
  ON public.user_setting_values (user_id, profile_id, library_id, key) WHERE scope = 'profile_library';
CREATE UNIQUE INDEX user_setting_values_profile_series_uq
  ON public.user_setting_values (user_id, profile_id, series_id, key) WHERE scope = 'profile_series';

-- The hot path: one query per resolution request collects every candidate row
-- for a key set at one identity, and the resolver ranks them in Go.
CREATE INDEX user_setting_values_resolution_idx
  ON public.user_setting_values (user_id, profile_id, key, scope);
CREATE INDEX user_setting_values_series_idx
  ON public.user_setting_values (user_id, profile_id, series_id);
CREATE INDEX user_setting_values_library_idx
  ON public.user_setting_values (user_id, profile_id, library_id);

-- Mutation idempotency. Rows expire after 30 days; expires_at is not
-- self-enforcing, so a sweeper deletes them on the decisionlog_cleanup pattern.
CREATE TABLE public.user_setting_mutations (
    user_id      integer NOT NULL,
    mutation_id  text NOT NULL,
    request_hash text NOT NULL,
    result       jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    CONSTRAINT user_setting_mutations_pkey PRIMARY KEY (user_id, mutation_id),
    CONSTRAINT user_setting_mutations_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

CREATE INDEX user_setting_mutations_expiry_idx
  ON public.user_setting_mutations (expires_at);

-- Inert audit table for the one-time migration. It has no runtime read/write
-- API and is not an extension bag: it retains unrecognized or invalid historical
-- rows for operator inspection instead of silently deleting them. Bounded by the
-- migration rather than by traffic, so no sweeper applies.
CREATE TABLE public.user_setting_migration_rejects (
    id           bigserial PRIMARY KEY,
    user_id      integer NOT NULL,
    source_table text NOT NULL,
    source_key   text NOT NULL,
    identity     jsonb NOT NULL,
    value        text,
    reason       text NOT NULL,
    recorded_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_setting_migration_rejects_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

CREATE INDEX user_setting_migration_rejects_user_idx
  ON public.user_setting_migration_rejects (user_id, source_table);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.user_setting_migration_rejects;
DROP TABLE IF EXISTS public.user_setting_mutations;
DROP TABLE IF EXISTS public.user_setting_values;
-- +goose StatementEnd
