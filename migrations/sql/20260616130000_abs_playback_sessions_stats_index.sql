-- +goose Up
-- +goose StatementBegin
-- AggregateStats and ListClosedSessions filter by (user_id, profile_id) and
-- group/order by started_at. The existing (user_id, profile_id) index leaves
-- started_at unindexed, so each /me/listening-stats and /me/stats/year call
-- sorts the user's full session history. Extend the prefix to cover started_at.
CREATE INDEX IF NOT EXISTS idx_abs_playback_sessions_user_profile_started
ON public.abs_playback_sessions USING btree (user_id, profile_id, started_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_abs_playback_sessions_user_profile_started;
-- +goose StatementEnd
