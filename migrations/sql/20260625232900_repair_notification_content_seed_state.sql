-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.notification_content_seed_state (
    library_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    kind text NOT NULL,
    seeded_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_content_seed_state_pkey PRIMARY KEY (library_id, kind)
);

DO $$
BEGIN
    IF to_regclass('public.notification_movie_seed_state') IS NOT NULL THEN
        EXECUTE $seed$
            INSERT INTO public.notification_content_seed_state (library_id, kind, seeded_at)
            SELECT library_id, 'movie', seeded_at
            FROM public.notification_movie_seed_state
            ON CONFLICT (library_id, kind) DO NOTHING
        $seed$;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- No-op: the original notification-channel migration owns this table on clean
-- databases; this repair only restores it for databases with older applied SQL.
SELECT 1;
-- +goose StatementEnd
