-- +goose Up
-- Audiobook enrichment had no state of its own. Ebooks have
-- ebook_enrichment_state and manga has manga_enrichment_state, but audiobooks
-- carried only media_items.last_refreshed: a single stamp with no attempt
-- count, no error classification, no backoff and no way to park a no-match and
-- revisit it later. That one stamp had to mean "matched", "genuinely
-- unmatchable" and "the provider was down that minute" all at once.
--
-- This sits deliberately between the two existing tables. It is not the full
-- ebook lease queue: audiobook sweeps run from a single task-manager goroutine
-- and claimBatch takes no row locks, so claim_token/lease_until would be
-- machinery with nothing to coordinate. It is more than the manga table, which
-- counts failures and nothing else.
--
-- last_refreshed stays authoritative for eligibility, so this migration changes
-- no behaviour on its own. The table records why an item reached its current
-- state and when it may be tried again.
CREATE TABLE IF NOT EXISTS audiobook_enrichment_state (
    content_id       TEXT PRIMARY KEY
                     REFERENCES media_items (content_id) ON DELETE CASCADE,

    -- Attempts that actually reached a provider. In ebook_enrichment_state this
    -- sits at 0 even on terminal rows, which is why the 2026-07-20 bulk
    -- no_match event could not be told apart from "never tried"; here it always
    -- increments.
    attempts         INTEGER NOT NULL DEFAULT 0,

    -- 'success' | 'no_match' | 'skipped', matching the ebook vocabulary.
    -- NULL means never attempted.
    outcome          TEXT,

    -- 'transient' | 'rate_limited' | 'permanent'. A rate-limited answer is
    -- indistinguishable from a genuine no-match in the outcome column alone,
    -- which is exactly how the ebook backlog became unreadable.
    last_error_class TEXT,
    last_error       TEXT,

    -- When the item may be attempted again. NULL means "not parked".
    next_attempt_at  TIMESTAMPTZ,
    last_attempt_at  TIMESTAMPTZ,
    completed_at     TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The sweep's question is "what is due now", so index parked items by due time.
-- Partial: rows with no next_attempt_at are never selected by it.
CREATE INDEX IF NOT EXISTS audiobook_enrichment_state_due_idx
    ON audiobook_enrichment_state (next_attempt_at)
    WHERE next_attempt_at IS NOT NULL;

-- Backlog reporting is by outcome ("how many no_match", "how many succeeded
-- today"), which would otherwise scan the whole table.
CREATE INDEX IF NOT EXISTS audiobook_enrichment_state_outcome_idx
    ON audiobook_enrichment_state (outcome);

-- +goose Down
DROP INDEX IF EXISTS audiobook_enrichment_state_outcome_idx;
DROP INDEX IF EXISTS audiobook_enrichment_state_due_idx;
DROP TABLE IF EXISTS audiobook_enrichment_state;
