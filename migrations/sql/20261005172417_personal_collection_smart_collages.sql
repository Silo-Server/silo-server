-- +goose Up
-- A smart personal collection's collage comes from its query's first matches,
-- which are too costly to read on every list read. A background refresh reads
-- them and records here which stored collage each viewer access sees, so a
-- list read finds it by key alone. variant_key is NULL when that viewer can
-- see no match with a poster. A row goes with its collection, and with its
-- collage when the server retires that collage as unused.
CREATE TABLE public.user_personal_collection_smart_collages (
    user_id integer NOT NULL,
    collection_id text NOT NULL,
    -- A hash of the viewer's access filter.
    access_key text NOT NULL,
    -- A hash of the query definition the collage was chosen for.
    definition_key text NOT NULL,
    variant_key text,
    refreshed_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, collection_id, access_key),
    FOREIGN KEY (user_id, collection_id)
        REFERENCES public.user_personal_collections (user_id, id) ON DELETE CASCADE,
    FOREIGN KEY (user_id, collection_id, variant_key)
        REFERENCES public.user_personal_collection_poster_variants (user_id, collection_id, variant_key) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE IF EXISTS public.user_personal_collection_smart_collages;
