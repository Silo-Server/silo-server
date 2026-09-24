-- Episodes a series monitor must not register again on its device.
--
-- Deleting a managed episode download removes its downloads row, so the next
-- monitor sync saw the episode as missing and registered it again. Deleting
-- an episode of a monitored series records it here instead; monitor syncs skip
-- it until the user downloads the episode explicitly, which clears the row.
-- Creating the monitor again (the native create or the bridge re-monitor)
-- clears all of its rows, and deleting the monitor (or its device) drops them
-- through the FK cascade.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.download_subscription_exclusions (
    subscription_id text        NOT NULL REFERENCES public.download_subscriptions(id) ON DELETE CASCADE,
    episode_id      text        NOT NULL,   -- content_id of the deleted episode
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT download_subscription_exclusions_pkey PRIMARY KEY (subscription_id, episode_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.download_subscription_exclusions;
-- +goose StatementEnd
