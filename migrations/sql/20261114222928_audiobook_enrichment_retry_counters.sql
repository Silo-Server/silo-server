-- +goose Up
-- audiobook_enrichment_state.attempts carried two counters at once: the
-- consecutive clean no-match streak, and a failure count that reset that
-- streak. A provider that failed once between no-matches kept an item from
-- ever reaching a terminal state, and failures alone had no cap at all.
--
-- attempts is now the total number of recorded attempts. no_matches is the
-- clean no-match count, which provider failures leave alone. failures is the
-- consecutive provider-failure count, cleared by a clean provider answer; an
-- item whose failures reach the cap gets the terminal outcome 'failed'.
ALTER TABLE audiobook_enrichment_state
    ADD COLUMN IF NOT EXISTS no_matches integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS failures integer NOT NULL DEFAULT 0;

-- A no_match row's attempts was its streak. A row with no outcome and an
-- error class last failed, but its attempts also counted earlier no-matches
-- and skips, so it starts the new failure count at one.
UPDATE audiobook_enrichment_state
SET no_matches = CASE WHEN outcome = 'no_match' THEN attempts ELSE 0 END,
    failures   = CASE WHEN outcome IS NULL AND last_error_class IS NOT NULL THEN 1 ELSE 0 END
WHERE attempts > 0;

-- +goose Down
-- The older code has no terminal failure outcome; hand those rows back to it
-- as pending failures.
UPDATE audiobook_enrichment_state
SET outcome = NULL, completed_at = NULL
WHERE outcome = 'failed';

-- The older code reads attempts as the clean no-match count, and as the
-- backoff multiplier for failures. Restore it from the no-match count on every
-- row that is not finished, before the new counters are dropped.
UPDATE audiobook_enrichment_state
SET attempts = no_matches
WHERE outcome IS DISTINCT FROM 'success';

ALTER TABLE audiobook_enrichment_state
    DROP COLUMN IF EXISTS failures,
    DROP COLUMN IF EXISTS no_matches;
