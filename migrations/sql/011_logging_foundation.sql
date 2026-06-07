-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.activity_log
    ADD COLUMN IF NOT EXISTS request_id text,
    ADD COLUMN IF NOT EXISTS node_id text,
    ADD COLUMN IF NOT EXISTS path_pattern text;

CREATE INDEX IF NOT EXISTS idx_activity_log_request_id ON public.activity_log USING btree (request_id, "timestamp" DESC);

CREATE TABLE IF NOT EXISTS public.operational_logs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    level text NOT NULL,
    component text NOT NULL,
    message text NOT NULL,
    request_id text,
    user_id integer,
    session_id text,
    client_ip inet,
    node_id text,
    attrs jsonb DEFAULT '{}'::jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_operational_logs_timestamp ON public.operational_logs USING btree ("timestamp" DESC);
CREATE INDEX IF NOT EXISTS idx_operational_logs_level ON public.operational_logs USING btree (level, "timestamp" DESC);
CREATE INDEX IF NOT EXISTS idx_operational_logs_component ON public.operational_logs USING btree (component, "timestamp" DESC);
CREATE INDEX IF NOT EXISTS idx_operational_logs_request_id ON public.operational_logs USING btree (request_id, "timestamp" DESC);
CREATE INDEX IF NOT EXISTS idx_operational_logs_user_id ON public.operational_logs USING btree (user_id, "timestamp" DESC);
CREATE INDEX IF NOT EXISTS idx_operational_logs_session_id ON public.operational_logs USING btree (session_id, "timestamp" DESC);

ALTER TABLE ONLY public.operational_logs
    ADD CONSTRAINT operational_logs_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;
-- +goose StatementEnd
