-- +goose Up
-- +goose StatementBegin
-- users.role and users.enabled are nullable in the schema and non-nullable
-- everywhere else.
--
-- models.User declares Role as string and Enabled as bool, and
-- auth.UserRepository selects both in allColumns and scans them straight into
-- those fields. A NULL in either column does not degrade gracefully: pgx cannot
-- scan NULL into string or bool, so the row fails to scan -- and because
-- scanUsers iterates one result set, a single NULL row breaks the whole admin
-- user list, not just that account. Every read path already assumes NOT NULL;
-- this makes the schema say so.
--
-- Backfill values are the ones the rest of the system already treats as the
-- default for a row that does not specify:
--
--   * role -> 'user'. UserRepository.Create always writes role explicitly, and
--     every caller passes models.RoleUser or models.RoleAdmin, so a NULL can
--     only come from a hand-edited or pre-role row. 'user' is also the
--     fail-closed choice: every privileged path in internal/auth tests
--     role == "admin", so a recovered row gets the unprivileged role.
--   * enabled -> true. The column has carried DEFAULT true since the 001
--     baseline and Create never writes it, relying on that default; true is
--     therefore what the schema itself says an unspecified row means.
--
-- No DEFAULT is added for role, deliberately. enabled keeps the DEFAULT true it
-- already has because Create depends on it, but role has never had one and every
-- insert supplies it; a default would turn a caller that forgot the column into
-- a silently created ordinary account instead of an error.
UPDATE public.users SET role = 'user' WHERE role IS NULL;
UPDATE public.users SET enabled = true WHERE enabled IS NULL;

ALTER TABLE public.users ALTER COLUMN role SET NOT NULL;
ALTER TABLE public.users ALTER COLUMN enabled SET NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restore nullability. enabled keeps its DEFAULT true, which the Up did not
-- touch.
--
-- Lossy in reverse, deliberately: the Up overwrote the NULLs in place and
-- recorded nothing about which rows they were, so rolling back allows NULLs
-- again but does not put them back. The distinction only matters if something
-- depended on telling "never set" apart from 'user'/true, which nothing does.
ALTER TABLE public.users ALTER COLUMN enabled DROP NOT NULL;
ALTER TABLE public.users ALTER COLUMN role DROP NOT NULL;
-- +goose StatementEnd
