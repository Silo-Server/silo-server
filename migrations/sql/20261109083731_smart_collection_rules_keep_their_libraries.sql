-- +goose Up
-- A server smart collection's rules now draw from their own library_ids, or
-- every library when they name none. Until now viewers got the rules narrowed
-- to the libraries the collection shows in (library_collection_libraries,
-- else the legacy library_id), and rules naming no library took those
-- libraries. Write that effective list into the rules so every existing
-- collection keeps the titles it had. catalog.PinSmartRulesToCollectionLibraries
-- does the same for collections a restore of an older backup creates.
--
-- Legacy section-filter rules (filter_type, filter_library_id,
-- filter_library_ids) name their libraries in filter_library_ids, which the
-- catalog reads instead of library_ids, so they get the list in both keys.
--
-- Rules whose libraries miss every library the collection shows in matched
-- nothing before. They now draw from the libraries they name, which is what
-- the editor and its preview already showed. An empty list can't stand for
-- "nothing" any more, since it means every library. Rules with library_ids are
-- left as they are; legacy rules get the libraries they name in library_ids.
WITH smart AS (
    SELECT
        lc.id,
        CASE WHEN jsonb_typeof(lc.query_definition) = 'object' THEN lc.query_definition ELSE '{}'::jsonb END AS rules,
        COALESCE(
            (SELECT array_agg(DISTINCT lcl.library_id)
             FROM library_collection_libraries lcl
             WHERE lcl.collection_id = lc.id),
            ARRAY[lc.library_id]
        ) AS placement
    FROM library_collections lc
    WHERE lower(btrim(lc.collection_type)) = 'smart'
      AND jsonb_typeof(lc.query_definition) IN ('object', 'null')
),
classified AS (
    SELECT
        id,
        rules,
        placement,
        (rules ? 'filter_type' OR rules ? 'filter_library_id' OR rules ? 'filter_library_ids') AS legacy
    FROM smart
),
named AS (
    SELECT
        c.id,
        c.rules,
        c.placement,
        c.legacy,
        ARRAY(
            SELECT (entry #>> '{}')::integer
            FROM jsonb_array_elements(
                CASE
                    WHEN c.legacy AND jsonb_typeof(c.rules -> 'filter_library_ids') = 'array'
                        THEN c.rules -> 'filter_library_ids'
                    WHEN NOT c.legacy AND jsonb_typeof(c.rules -> 'library_ids') = 'array'
                        THEN c.rules -> 'library_ids'
                    ELSE '[]'::jsonb
                END
            ) AS entry
            WHERE jsonb_typeof(entry) = 'number' AND (entry #>> '{}') ~ '^[1-9][0-9]{0,8}$'
        ) || CASE
            WHEN c.legacy
             AND jsonb_typeof(c.rules -> 'filter_library_id') = 'number'
             AND (c.rules ->> 'filter_library_id') ~ '^[1-9][0-9]{0,8}$'
                THEN ARRAY[(c.rules ->> 'filter_library_id')::integer]
            ELSE ARRAY[]::integer[]
        END AS ids
    FROM classified c
),
effective AS (
    SELECT
        id,
        rules,
        legacy,
        to_jsonb(CASE
            -- Legacy rules naming none of the collection's libraries keep the
            -- libraries they name, written to library_ids as well: some readers
            -- see only that key, and without it they would read every library.
            WHEN legacy AND cardinality(ids) > 0 AND NOT (placement && ids) THEN ARRAY(
                SELECT DISTINCT library_id
                FROM unnest(ids) AS library_id
                ORDER BY library_id
            )
            ELSE ARRAY(
                SELECT DISTINCT library_id
                FROM unnest(placement) AS library_id
                WHERE cardinality(ids) = 0 OR library_id = ANY (ids)
                ORDER BY library_id
            )
        END) AS library_ids
    FROM named
),
pinned AS (
    SELECT
        id,
        CASE
            WHEN legacy THEN jsonb_set(jsonb_set(rules, '{library_ids}', library_ids), '{filter_library_ids}', library_ids) - 'filter_library_id'
            ELSE jsonb_set(rules, '{library_ids}', library_ids)
        END AS rules
    FROM effective
    WHERE jsonb_array_length(library_ids) > 0
)
-- Only rows whose rules change, so the revision trigger leaves the others'
-- open pages alone.
UPDATE library_collections lc
SET query_definition = p.rules
FROM pinned p
WHERE lc.id = p.id
  AND lc.query_definition IS DISTINCT FROM p.rules;

-- +goose Down
-- Nothing to undo: the lists written above are what the older read paths
-- already narrowed these rules to, so they read the same titles either way.
SELECT 1;
