-- +goose Up
-- +goose StatementBegin
-- Hourly API request counts per sign-in session, summarized from activity_log
-- for the admin dashboard's API clients card. Media transfer requests are left
-- out, as the session quota leaves them out. refused counts 429 responses.
-- Rows are rewritten while their hour is recent and pruned after 30 days; the
-- primary key's leading hour serves both the window reads and the pruning.
CREATE TABLE public.api_client_usage_hourly (
    hour timestamp with time zone NOT NULL,
    session_id text NOT NULL,
    user_id integer,
    user_agent text NOT NULL DEFAULT '',
    requests integer NOT NULL,
    refused integer NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, session_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.api_client_usage_hourly;
-- +goose StatementEnd
