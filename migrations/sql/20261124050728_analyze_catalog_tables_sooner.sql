-- Autovacuum re-analyzes a table once more than 50 rows plus 10% of the table
-- have changed since its last analyze (autovacuum_analyze_scale_factor = 0.1).
-- A library added to a large catalog is usually well under 10% of these
-- tables, so their statistics never learn about it: the planner estimates the
-- new library at about one row and picks plans meant for a handful of rows. On
-- a large catalog, a TV library added later left its Recently Added rows
-- timing out until the tables were analyzed by hand.
--
-- These are the tables that library- and folder-scoped catalog queries filter
-- and join on. A 1% scale factor makes autovacuum, which visits each database
-- about once a minute by default, analyze them once a new library is about 1% of the
-- table instead of 10%. The base of 50 rows stays, so a small table is analyzed
-- after about as many changes as before. A fixed threshold with no scale factor
-- would have to be large enough not to analyze big tables constantly, and a
-- small install would then wait longer than it does today.
--
-- An analyze samples at most 300 rows per unit of statistics target, read from
-- at most as many pages (30,000 rows from at most 30,000 pages, about 240 MB, at
-- the default of 100), whatever the table's size, and
-- autovacuum analyzes a table at most once per pass. The vacuum thresholds are
-- unchanged.
--
-- Setting a storage parameter takes a SHARE UPDATE EXCLUSIVE lock, which does
-- not block reads or writes but waits for a VACUUM or ANALYZE running on the
-- table. A regular autovacuum gives way within a second; a manual VACUUM or
-- ANALYZE or an autovacuum preventing transaction ID wraparound does not.
-- Migrations run before the server listens, so the wait holds up only startup:
-- this server's, and any other server waiting on the migration lock. Each
-- table waits at most a minute, six minutes in all. If a lock is not free
-- by then, the migration rolls back with nothing changed, startup fails with a
-- lock timeout, and the next start tries again.

-- +goose Up
SET LOCAL lock_timeout = '1min';
ALTER TABLE public.media_items SET (autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE public.media_item_libraries SET (autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE public.episodes SET (autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE public.episode_libraries SET (autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE public.media_files SET (autovacuum_analyze_scale_factor = 0.01);
ALTER TABLE public.episode_catalog_entries SET (autovacuum_analyze_scale_factor = 0.01);

-- +goose Down
SET LOCAL lock_timeout = '1min';
ALTER TABLE public.media_items RESET (autovacuum_analyze_scale_factor);
ALTER TABLE public.media_item_libraries RESET (autovacuum_analyze_scale_factor);
ALTER TABLE public.episodes RESET (autovacuum_analyze_scale_factor);
ALTER TABLE public.episode_libraries RESET (autovacuum_analyze_scale_factor);
ALTER TABLE public.media_files RESET (autovacuum_analyze_scale_factor);
ALTER TABLE public.episode_catalog_entries RESET (autovacuum_analyze_scale_factor);
