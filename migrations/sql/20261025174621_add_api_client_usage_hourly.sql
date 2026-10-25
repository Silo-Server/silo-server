-- +goose Up
-- +goose StatementBegin
-- Hourly API request counts per sign-in session, summarized from activity_log
-- for the admin dashboard's API clients card. It leaves out what the session
-- quota leaves out: playback and media transfer routes, admins, and requests
-- made while impersonating. Hours are UTC. refused counts 429 responses.
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
