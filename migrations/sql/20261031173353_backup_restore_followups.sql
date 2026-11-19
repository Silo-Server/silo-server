-- +goose Up
-- Follow-up work a selective restore owes after an item commits: a restored
-- library's sections, provider chain, monitoring, and first scan, or a
-- restored plugin's lifecycle event and provider-chain sync. The row is
-- written in the item's transaction and deleted once the work is done, so a
-- crash between the commit and the work leaves it for the next restore run.
CREATE TABLE public.backup_restore_followups (
    kind text NOT NULL CHECK (kind IN ('library_created', 'plugin_installed')),
    target_id bigint NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, target_id)
);

-- +goose Down
DROP TABLE IF EXISTS public.backup_restore_followups;
