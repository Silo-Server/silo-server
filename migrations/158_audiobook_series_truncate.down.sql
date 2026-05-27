-- TRUNCATE cannot be undone — the table contents were discarded
-- intentionally because they were 87% noise from a parser bug. Down
-- migration is a no-op; the next library scan after rolling back the
-- parser fix would repopulate the table with the same polluted data,
-- which is not a desirable rollback target.
SELECT 1 WHERE FALSE; -- no-op
