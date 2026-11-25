-- +goose Up
-- +goose StatementBegin
-- Edits made on this server, outside a profile set, to profiles that came
-- from one. One row per profile (by its sync ID) and field: an item field
-- name, settings.<key>, or '' for the profile's removal.
--
-- by = 'account' is the person's own edit (a mark), which the next accepted
-- set settles; by = 'owner' is an administrator's (an override), which a set
-- leaves alone. A later edit of the field by either side replaces the row.
-- actor_user_id is the account that made the edit: the account itself, or
-- the administrator's. replaced_owner marks the person's edit of a field the
-- owner had set. An accepted set deletes the marks it settles, except those
-- with replaced_owner, which it keeps with settled_at as a record that the
-- person ended an override. A settled row is history only.
--
-- revision comes from a sequence on every write, so an edit back to an old
-- value still names a new edit. profile_name is the removed profile's name,
-- on removal rows only.
--
-- No foreign keys: recording an edit never locks the account row, and
-- deleting an account clears its rows itself after the account is deleted.
CREATE SEQUENCE public.profile_set_edits_revision_seq;
CREATE TABLE public.profile_set_edits (
    user_id        integer     NOT NULL,
    sync_id        uuid        NOT NULL,
    field          text        NOT NULL,
    by             text        NOT NULL,
    actor_user_id  integer,
    at             timestamptz NOT NULL DEFAULT now(),
    revision       bigint      NOT NULL DEFAULT nextval('public.profile_set_edits_revision_seq'),
    replaced_owner boolean     NOT NULL DEFAULT false,
    settled_at     timestamptz,
    profile_name   text,
    PRIMARY KEY (user_id, sync_id, field),
    CONSTRAINT profile_set_edits_by_check CHECK (by IN ('account', 'owner')),
    CONSTRAINT profile_set_edits_field_check CHECK (
        field IN ('', 'name', 'picture', 'pin', 'is_child', 'max_content_rating',
                  'max_advisory_age', 'require_advisory_age', 'requests_blocked')
        OR (field LIKE 'settings.%' AND length(field) BETWEEN 10 AND 200)),
    CONSTRAINT profile_set_edits_removal_name CHECK ((field = '') = (profile_name IS NOT NULL)),
    CONSTRAINT profile_set_edits_settled_check CHECK (
        settled_at IS NULL OR (by = 'account' AND replaced_owner))
);
ALTER SEQUENCE public.profile_set_edits_revision_seq OWNED BY public.profile_set_edits.revision;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.profile_set_edits;
DROP SEQUENCE IF EXISTS public.profile_set_edits_revision_seq;
-- +goose StatementEnd
