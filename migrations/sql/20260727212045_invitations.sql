-- +goose Up
-- +goose StatementBegin
-- Emailed, pre-provisioned invitations: a single-use capability token bound
-- to one email address, carrying the access decisions the admin made at send
-- time. Deliberately creates no users row until accepted, so a mistyped
-- address cannot squat a username or leave a ghost account.
-- Spec: docs/superpowers/specs/2026-07-27-invitations-and-onboarding-design.md
CREATE TABLE public.invitations (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email             citext NOT NULL,
    -- SHA-256 hex of the claim token. The raw token exists only in the sent
    -- email; a database dump yields no usable links.
    token_hash        text NOT NULL UNIQUE,
    -- Pre-bound access, applied verbatim at accept time.
    role              text NOT NULL DEFAULT 'user'
        CONSTRAINT invitations_role_check CHECK (role IN ('user', 'admin')),
    access_group_id   bigint REFERENCES public.access_groups(id) ON DELETE SET NULL,
    library_ids       integer[],           -- NULL = all libraries
    create_profile    boolean NOT NULL DEFAULT true,
    show_tour         boolean NOT NULL DEFAULT true,
    note              text NOT NULL DEFAULT '',
    -- Lifecycle. Status (pending/accepted/expired/revoked) is derived from
    -- these timestamps at read time; there is no status column to drift.
    invited_by        bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    expires_at        timestamptz NOT NULL,
    accepted_at       timestamptz,
    accepted_user_id  bigint REFERENCES public.users(id) ON DELETE SET NULL,
    revoked_at        timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX invitations_email_idx ON public.invitations (email);

-- At most one live invitation per address: re-inviting supersedes rather
-- than accumulating parallel valid tokens for the same person. Expired rows
-- still match this predicate on purpose — creating a replacement revokes
-- the stale pending row first, keeping "resend supersedes" auditable.
CREATE UNIQUE INDEX invitations_one_pending_idx ON public.invitations (email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE public.invitations;
-- +goose StatementEnd
