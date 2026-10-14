-- +goose Up
-- Access groups, account overrides and invitations name libraries in integer
-- arrays, which no foreign key covers, so deleting a library used to leave its
-- ID behind: the admin pages listed it as "#20" and counted it as a library,
-- and saving the policy kept it. Profile allowlists are a join table and
-- already cascade.
--
-- existing_library_ids keeps the IDs that still name a library, in their
-- order. A list left with none becomes empty, never NULL: NULL means every
-- library, so pruning must not widen access.
-- +goose StatementBegin
CREATE FUNCTION public.existing_library_ids(ids integer[])
RETURNS integer[]
LANGUAGE sql
STABLE
STRICT
AS $$
    SELECT COALESCE(array_agg(listed.id ORDER BY listed.position), '{}')
    FROM unnest(ids) WITH ORDINALITY AS listed(id, position)
    WHERE EXISTS (SELECT 1 FROM public.media_folders f WHERE f.id = listed.id)
$$;
-- +goose StatementEnd

-- Prune on every library delete, in the deleting statement, so no path can
-- leave a reference behind. Changing users.library_ids does not bump
-- access_policy_revision, so members stay signed in; the edited rows still get
-- a new admin or configuration revision, so an admin form opened before the
-- delete gets a conflict instead of saving the deleted library back.
-- +goose StatementBegin
CREATE FUNCTION public.prune_deleted_library_access_references()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    deleted integer[];
BEGIN
    -- library_ids is integer[], so a library past its range can't be listed.
    SELECT array_agg(old_rows.id)::integer[] INTO deleted
    FROM old_rows WHERE old_rows.id <= 2147483647;
    IF deleted IS NULL THEN
        RETURN NULL; -- nothing listable deleted, or a retry found it gone
    END IF;
    UPDATE public.access_groups
    SET library_ids = public.existing_library_ids(library_ids), updated_at = now()
    WHERE library_ids && deleted;
    UPDATE public.users
    SET library_ids = public.existing_library_ids(library_ids), updated_at = now()
    WHERE library_ids && deleted;
    UPDATE public.invitations
    SET library_ids = public.existing_library_ids(library_ids), updated_at = now()
    WHERE library_ids && deleted;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER media_folders_prune_access_references
    AFTER DELETE ON public.media_folders
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.prune_deleted_library_access_references();

-- Every write keeps only IDs that name a library, whichever API or node made
-- it, as a foreign key would: it locks the named libraries, so a concurrent
-- delete either waits for the write and then prunes it, or commits first and
-- its ID is dropped here. A write that only removes IDs adds nothing to check,
-- which keeps the prune above from locking the libraries it is not deleting.
-- +goose StatementBegin
CREATE FUNCTION public.keep_existing_library_ids()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.library_ids <@ OLD.library_ids THEN
        RETURN NEW;
    END IF;
    PERFORM 1 FROM public.media_folders WHERE id = ANY (NEW.library_ids) FOR KEY SHARE;
    NEW.library_ids := public.existing_library_ids(NEW.library_ids);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER access_groups_keep_existing_library_ids
    BEFORE INSERT OR UPDATE OF library_ids ON public.access_groups
    FOR EACH ROW WHEN (NEW.library_ids IS NOT NULL)
    EXECUTE FUNCTION public.keep_existing_library_ids();
CREATE TRIGGER users_keep_existing_library_ids
    BEFORE INSERT OR UPDATE OF library_ids ON public.users
    FOR EACH ROW WHEN (NEW.library_ids IS NOT NULL)
    EXECUTE FUNCTION public.keep_existing_library_ids();
CREATE TRIGGER invitations_keep_existing_library_ids
    BEFORE INSERT OR UPDATE OF library_ids ON public.invitations
    FOR EACH ROW WHEN (NEW.library_ids IS NOT NULL)
    EXECUTE FUNCTION public.keep_existing_library_ids();

-- Repair the references earlier deletes left behind.
UPDATE public.access_groups
SET library_ids = public.existing_library_ids(library_ids), updated_at = now()
WHERE library_ids IS DISTINCT FROM public.existing_library_ids(library_ids);
UPDATE public.users
SET library_ids = public.existing_library_ids(library_ids), updated_at = now()
WHERE library_ids IS DISTINCT FROM public.existing_library_ids(library_ids);
UPDATE public.invitations
SET library_ids = public.existing_library_ids(library_ids), updated_at = now()
WHERE library_ids IS DISTINCT FROM public.existing_library_ids(library_ids);

-- +goose Down
-- The repaired IDs named no library, so there is nothing to restore.
DROP TRIGGER IF EXISTS invitations_keep_existing_library_ids ON public.invitations;
DROP TRIGGER IF EXISTS users_keep_existing_library_ids ON public.users;
DROP TRIGGER IF EXISTS access_groups_keep_existing_library_ids ON public.access_groups;
DROP FUNCTION IF EXISTS public.keep_existing_library_ids();
DROP TRIGGER IF EXISTS media_folders_prune_access_references ON public.media_folders;
DROP FUNCTION IF EXISTS public.prune_deleted_library_access_references();
DROP FUNCTION IF EXISTS public.existing_library_ids(integer[]);
