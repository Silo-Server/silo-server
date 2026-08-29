-- +goose Up
-- reopen_image_ladder_backfill_v2 declared a plpgsql variable named
-- image_type while its manifest probe filters on the column
-- artwork_revision_gc_candidates.image_type. The bare right-hand reference in
-- "manifest.image_type = image_type" matches both, and plpgsql's default
-- variable_conflict=error raises 42702 the first time the probe runs — which
-- is only once backfilled_version has reached 2, so every local cached-path
-- publication on a v2-complete deployment fails outright instead of reopening
-- the fence. Renaming the variable removes the collision; behavior is
-- otherwise identical.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.reopen_image_ladder_backfill_v2()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    new_row jsonb := to_jsonb(NEW);
    old_row jsonb;
    arg_index integer := 0;
    path_column text;
    manifest_image_type text;
    rung_pattern text;
    cached_path text;
    previous_path text;
    state_version integer;
    changed_cached_path boolean := false;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_row := to_jsonb(OLD);
    END IF;

    -- Find an affected local cached-path publication before taking the shared
    -- singleton lock. The lock is still taken even when the state is below v2:
    -- that closes the ordering with a concurrent final confirmation.
    WHILE arg_index < TG_NARGS LOOP
        path_column := TG_ARGV[arg_index];
        cached_path := new_row ->> path_column;
        previous_path := CASE WHEN old_row IS NULL THEN NULL ELSE old_row ->> path_column END;
        IF COALESCE(BTRIM(cached_path), '') <> ''
           AND cached_path NOT LIKE '%://%'
           AND (TG_OP = 'INSERT' OR cached_path IS DISTINCT FROM previous_path) THEN
            changed_cached_path := true;
            EXIT;
        END IF;
        arg_index := arg_index + 3;
    END LOOP;

    IF NOT changed_cached_path THEN
        RETURN NEW;
    END IF;

    SELECT backfilled_version
    INTO state_version
    FROM public.image_ladder_backfill_state
    WHERE id = 1
    FOR UPDATE;

    IF state_version < 2 THEN
        RETURN NEW;
    END IF;

    arg_index := 0;
    WHILE arg_index < TG_NARGS LOOP
        path_column := TG_ARGV[arg_index];
        manifest_image_type := TG_ARGV[arg_index + 1];
        rung_pattern := TG_ARGV[arg_index + 2];
        cached_path := new_row ->> path_column;
        previous_path := CASE WHEN old_row IS NULL THEN NULL ELSE old_row ->> path_column END;

        IF COALESCE(BTRIM(cached_path), '') <> ''
           AND cached_path NOT LIKE '%://%'
           AND (TG_OP = 'INSERT' OR cached_path IS DISTINCT FROM previous_path)
           AND NOT EXISTS (
               SELECT 1
               FROM public.artwork_revision_gc_candidates manifest
               WHERE manifest.original_path = cached_path
                 AND manifest.image_type = manifest_image_type
                 AND EXISTS (
                     SELECT 1
                     FROM unnest(manifest.object_keys) object_key
                     WHERE object_key LIKE rung_pattern
                 )
           ) THEN
            UPDATE public.image_ladder_backfill_state
            SET backfilled_version = LEAST(backfilled_version, 1),
                last_attempt_at = NULL,
                updated_at = NOW()
            WHERE id = 1;
            RETURN NEW;
        END IF;

        arg_index := arg_index + 3;
    END LOOP;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- Restores the previous definition verbatim, including the ambiguous
-- image_type variable this migration exists to remove.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.reopen_image_ladder_backfill_v2()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    new_row jsonb := to_jsonb(NEW);
    old_row jsonb;
    arg_index integer := 0;
    path_column text;
    image_type text;
    rung_pattern text;
    cached_path text;
    previous_path text;
    state_version integer;
    changed_cached_path boolean := false;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_row := to_jsonb(OLD);
    END IF;

    WHILE arg_index < TG_NARGS LOOP
        path_column := TG_ARGV[arg_index];
        cached_path := new_row ->> path_column;
        previous_path := CASE WHEN old_row IS NULL THEN NULL ELSE old_row ->> path_column END;
        IF COALESCE(BTRIM(cached_path), '') <> ''
           AND cached_path NOT LIKE '%://%'
           AND (TG_OP = 'INSERT' OR cached_path IS DISTINCT FROM previous_path) THEN
            changed_cached_path := true;
            EXIT;
        END IF;
        arg_index := arg_index + 3;
    END LOOP;

    IF NOT changed_cached_path THEN
        RETURN NEW;
    END IF;

    SELECT backfilled_version
    INTO state_version
    FROM public.image_ladder_backfill_state
    WHERE id = 1
    FOR UPDATE;

    IF state_version < 2 THEN
        RETURN NEW;
    END IF;

    arg_index := 0;
    WHILE arg_index < TG_NARGS LOOP
        path_column := TG_ARGV[arg_index];
        image_type := TG_ARGV[arg_index + 1];
        rung_pattern := TG_ARGV[arg_index + 2];
        cached_path := new_row ->> path_column;
        previous_path := CASE WHEN old_row IS NULL THEN NULL ELSE old_row ->> path_column END;

        IF COALESCE(BTRIM(cached_path), '') <> ''
           AND cached_path NOT LIKE '%://%'
           AND (TG_OP = 'INSERT' OR cached_path IS DISTINCT FROM previous_path)
           AND NOT EXISTS (
               SELECT 1
               FROM public.artwork_revision_gc_candidates manifest
               WHERE manifest.original_path = cached_path
                 AND manifest.image_type = image_type
                 AND EXISTS (
                     SELECT 1
                     FROM unnest(manifest.object_keys) object_key
                     WHERE object_key LIKE rung_pattern
                 )
           ) THEN
            UPDATE public.image_ladder_backfill_state
            SET backfilled_version = LEAST(backfilled_version, 1),
                last_attempt_at = NULL,
                updated_at = NOW()
            WHERE id = 1;
            RETURN NEW;
        END IF;

        arg_index := arg_index + 3;
    END LOOP;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd
