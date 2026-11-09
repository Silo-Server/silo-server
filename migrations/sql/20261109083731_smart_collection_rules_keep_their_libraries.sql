-- +goose Up
-- A server smart collection's rules now draw from their own library_ids, or
-- every library when they name none. Until now viewers got the rules narrowed
-- to the libraries the collection shows in (library_collection_libraries,
-- else the legacy library_id), and rules naming no library took those
-- libraries. Write that effective list into the rules so every existing
-- collection keeps the titles it had.
--
-- Rules whose libraries miss every library the collection shows in matched
-- nothing before. They are left as they are and now draw from the libraries
-- they name, which is what the editor and its preview already showed. An empty
-- list can't stand for "nothing" any more, since it means every library.
WITH placement AS (
    SELECT
        lc.id,
        COALESCE(
            (SELECT array_agg(DISTINCT lcl.library_id)
             FROM library_collection_libraries lcl
             WHERE lcl.collection_id = lc.id),
            ARRAY[lc.library_id]
        ) AS library_ids
    FROM library_collections lc
    WHERE lower(btrim(lc.collection_type)) = 'smart'
      AND jsonb_typeof(lc.query_definition) IN ('object', 'null')
),
rules AS (
    SELECT
        p.id,
        p.library_ids AS placement,
        CASE
            WHEN jsonb_typeof(lc.query_definition -> 'library_ids') = 'array'
                THEN ARRAY(
                    SELECT (value #>> '{}')::integer
                    FROM jsonb_array_elements(lc.query_definition -> 'library_ids')
                    WHERE jsonb_typeof(value) = 'number'
                )
            ELSE ARRAY[]::integer[]
        END AS named
    FROM placement p
    JOIN library_collections lc ON lc.id = p.id
),
effective AS (
    SELECT
        id,
        to_jsonb(ARRAY(
            SELECT DISTINCT library_id
            FROM unnest(CASE WHEN cardinality(named) = 0 THEN placement ELSE named END) AS library_id
            WHERE cardinality(named) = 0 OR library_id = ANY (placement)
            ORDER BY library_id
        )) AS library_ids
    FROM rules
)
-- Only rows whose list changes, so the revision trigger leaves the others'
-- open pages alone.
UPDATE library_collections lc
SET query_definition = jsonb_set(
    CASE WHEN jsonb_typeof(lc.query_definition) = 'object' THEN lc.query_definition ELSE '{}'::jsonb END,
    '{library_ids}',
    e.library_ids
)
FROM effective e
WHERE lc.id = e.id
  AND jsonb_array_length(e.library_ids) > 0
  AND (lc.query_definition -> 'library_ids') IS DISTINCT FROM e.library_ids;

-- +goose Down
-- Nothing to undo: the lists written above are what the older read paths
-- already narrowed these rules to, so they read the same titles either way.
SELECT 1;
