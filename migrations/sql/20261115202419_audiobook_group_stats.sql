-- +goose Up
-- +goose StatementBegin
-- Precomputed Authors, Narrators and Series tabs of an audiobook library
-- (listAudiobookGroups). Grouping every credit of a large library on each
-- page takes seconds; this view turns a page into an indexed read. It counts
-- every audiobook of the library as of its last refresh, so only a viewer
-- without library or maturity limits reads it; the rest take the live query in
-- internal/catalog/audiobook_groups.go, whose group identity, counts and
-- cover order this view must keep matching.
--
-- content_ids holds the group's books, one entry per credit as the live
-- query counts them, ordered so its first four with a poster are the cover
-- stack: books with a poster first, then the cover order. Posters are read
-- at request time, since a replaced poster's old file can be collected.
-- Refreshed by the API servers (internal/catalog/audiobook_group_stats.go).
CREATE MATERIALIZED VIEW public.audiobook_group_stats AS
WITH books AS (
    SELECT
        mil.media_folder_id AS library_id,
        mi.content_id,
        NULLIF(mi.poster_path, '') IS NOT NULL AS has_poster,
        COALESCE(NULLIF(mi.sort_title, ''), mi.title) AS sort_title,
        COALESCE(afs.duration_seconds, 0)::bigint AS duration_seconds
    FROM public.media_items mi
    JOIN public.media_item_libraries mil ON mil.content_id = mi.content_id
    LEFT JOIN public.audiobook_item_file_stats afs
           ON afs.media_folder_id = mil.media_folder_id
          AND afs.content_id = mi.content_id
    WHERE mi.type = 'audiobook'
),
members AS (
    SELECT
        b.library_id,
        CASE ip.kind WHEN 7 THEN 'author' ELSE 'narrator' END AS group_by,
        LOWER(BTRIM(p.name)) AS group_key,
        BTRIM(p.name) AS name,
        b.content_id,
        b.has_poster,
        NULL::numeric AS series_index,
        b.sort_title,
        b.duration_seconds
    FROM books b
    JOIN public.item_people ip ON ip.content_id = b.content_id AND ip.kind IN (7, 8)
    JOIN public.people p ON p.id = ip.person_id
    UNION ALL
    SELECT
        b.library_id,
        'series',
        LOWER(BTRIM(s.series_name)),
        BTRIM(s.series_name),
        b.content_id,
        b.has_poster,
        s.series_index,
        b.sort_title,
        b.duration_seconds
    FROM books b
    JOIN public.audiobook_series s ON s.content_id = b.content_id
)
SELECT
    library_id,
    group_by,
    group_key,
    MIN(name) AS name,
    COUNT(*)::int AS item_count,
    COALESCE(SUM(duration_seconds), 0)::bigint AS total_duration_seconds,
    array_agg(content_id ORDER BY has_poster DESC, series_index NULLS LAST, sort_title, content_id) AS content_ids
FROM members
GROUP BY library_id, group_by, group_key
WITH DATA;

-- The unique index REFRESH ... CONCURRENTLY requires; it also serves the
-- name sort.
CREATE UNIQUE INDEX audiobook_group_stats_key
    ON public.audiobook_group_stats (library_id, group_by, group_key);
CREATE INDEX audiobook_group_stats_count
    ON public.audiobook_group_stats (library_id, group_by, item_count DESC, group_key);
CREATE INDEX audiobook_group_stats_duration
    ON public.audiobook_group_stats (library_id, group_by, total_duration_seconds DESC, group_key);
-- Without statistics the planner takes a large library's groups for a few
-- rows and sorts them all instead of reading the page from an index.
ANALYZE public.audiobook_group_stats;

-- When audiobook_group_stats was last refreshed, cluster-wide, so the API
-- servers refresh it once per interval rather than once each.
CREATE TABLE public.audiobook_group_stats_refresh (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    refreshed_at timestamp with time zone NOT NULL
);
INSERT INTO public.audiobook_group_stats_refresh (refreshed_at) VALUES (now());
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.audiobook_group_stats_refresh;
DROP MATERIALIZED VIEW IF EXISTS public.audiobook_group_stats;
-- +goose StatementEnd
