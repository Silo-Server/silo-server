-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.auth_sessions
  ADD COLUMN impersonator_user_id integer,
  ADD COLUMN impersonation_started_at timestamptz;

ALTER TABLE public.activity_log
  ADD COLUMN impersonator_user_id integer;

CREATE INDEX IF NOT EXISTS idx_auth_sessions_impersonator_user_id
  ON public.auth_sessions USING btree (impersonator_user_id)
  WHERE impersonator_user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_activity_log_impersonator_user_id
  ON public.activity_log USING btree (impersonator_user_id, "timestamp" DESC)
  WHERE impersonator_user_id IS NOT NULL;
-- +goose StatementEnd
