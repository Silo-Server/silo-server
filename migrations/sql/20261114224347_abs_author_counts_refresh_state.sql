-- +goose Up
-- +goose StatementBegin
-- When abs_audiobook_author_counts was last refreshed, cluster-wide. Every API
-- node runs the periodic refresh; the node that takes the advisory lock reads
-- this row and skips the refresh when another node did it within the interval,
-- so a cluster refreshes the view once per interval rather than once per node.
CREATE TABLE IF NOT EXISTS public.abs_author_counts_refresh (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    refreshed_at timestamp with time zone NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.abs_author_counts_refresh;
-- +goose StatementEnd
