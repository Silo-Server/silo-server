-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.operational_logs_partitioned (
    id bigint GENERATED ALWAYS AS IDENTITY,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    level text NOT NULL,
    component text NOT NULL,
    message text NOT NULL,
    request_id text,
    user_id integer,
    session_id text,
    client_ip inet,
    node_id text,
    attrs jsonb DEFAULT '{}'::jsonb NOT NULL,
    playback_session_id text,
    PRIMARY KEY (id, "timestamp")
) PARTITION BY RANGE ("timestamp");

DO $$
DECLARE
    min_ts timestamptz;
    max_ts timestamptz;
    period_start timestamptz;
    period_end timestamptz;
    table_name text;
BEGIN
    SELECT COALESCE(date_trunc('day', MIN("timestamp")), date_trunc('day', now())),
           GREATEST(COALESCE(date_trunc('day', MAX("timestamp")), date_trunc('day', now())), date_trunc('day', now()))
    INTO min_ts, max_ts
    FROM public.operational_logs;

    period_start := min_ts;
    period_end := max_ts + interval '4 days';

    WHILE period_start < period_end LOOP
        table_name := format('operational_logs_p_%s', to_char(period_start, 'YYYYMMDD'));
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.operational_logs_partitioned FOR VALUES FROM (%L) TO (%L)',
            table_name,
            period_start,
            period_start + interval '1 day'
        );
        period_start := period_start + interval '1 day';
    END LOOP;
END
$$;

CREATE TABLE public.operational_logs_default
    PARTITION OF public.operational_logs_partitioned DEFAULT;

INSERT INTO public.operational_logs_partitioned (
    id,
    "timestamp",
    level,
    component,
    message,
    request_id,
    user_id,
    session_id,
    client_ip,
    node_id,
    attrs,
    playback_session_id
)
OVERRIDING SYSTEM VALUE
SELECT
    id,
    "timestamp",
    level,
    component,
    message,
    request_id,
    user_id,
    session_id,
    client_ip,
    node_id,
    attrs,
    playback_session_id
FROM public.operational_logs;

DO $$
DECLARE
    seq_name text;
    max_id bigint;
BEGIN
    SELECT pg_get_serial_sequence('public.operational_logs_partitioned', 'id') INTO seq_name;
    SELECT MAX(id) INTO max_id FROM public.operational_logs_partitioned;
    IF max_id IS NULL THEN
        EXECUTE format('SELECT setval(%L, 1, false)', seq_name);
    ELSE
        EXECUTE format('SELECT setval(%L, %s, true)', seq_name, max_id);
    END IF;
END
$$;

ALTER TABLE public.operational_logs RENAME TO operational_logs_old;
ALTER TABLE public.operational_logs_partitioned RENAME TO operational_logs;
DROP TABLE public.operational_logs_old;

CREATE INDEX idx_operational_logs_timestamp_id
    ON public.operational_logs USING btree ("timestamp" DESC, id DESC);
CREATE INDEX idx_operational_logs_component_level
    ON public.operational_logs USING btree (component, level, "timestamp" DESC);
CREATE INDEX idx_operational_logs_request_id
    ON public.operational_logs USING btree (request_id, "timestamp" DESC);
CREATE INDEX idx_operational_logs_user_id
    ON public.operational_logs USING btree (user_id, "timestamp" DESC);
CREATE INDEX idx_operational_logs_session_id
    ON public.operational_logs USING btree (session_id, "timestamp" DESC);
CREATE INDEX idx_operational_logs_playback_session_id
    ON public.operational_logs USING btree (playback_session_id, "timestamp" DESC);

ALTER TABLE public.operational_logs
    ADD CONSTRAINT operational_logs_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER SEQUENCE public.operational_logs_partitioned_id_seq
    RENAME TO operational_logs_id_seq;

ALTER TABLE public.activity_log
    ALTER COLUMN id DROP DEFAULT;
ALTER SEQUENCE public.activity_log_id_seq
    OWNED BY NONE;

CREATE TABLE public.activity_log_partitioned (
    id bigint NOT NULL DEFAULT nextval('public.activity_log_id_seq'::regclass),
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    client_ip inet NOT NULL,
    user_id integer,
    session_id text,
    method text NOT NULL,
    path text NOT NULL,
    status_code integer,
    user_agent text,
    duration_ms integer,
    request_id text,
    node_id text,
    path_pattern text,
    playback_session_id text,
    impersonator_user_id integer,
    PRIMARY KEY (id, "timestamp")
) PARTITION BY RANGE ("timestamp");

DO $$
DECLARE
    min_ts timestamptz;
    max_ts timestamptz;
    period_start timestamptz;
    period_end timestamptz;
    table_name text;
BEGIN
    SELECT COALESCE(date_trunc('week', MIN("timestamp")), date_trunc('week', now())),
           GREATEST(COALESCE(date_trunc('week', MAX("timestamp")), date_trunc('week', now())), date_trunc('week', now()))
    INTO min_ts, max_ts
    FROM public.activity_log;

    period_start := min_ts;
    period_end := max_ts + interval '21 days';

    WHILE period_start < period_end LOOP
        table_name := format('activity_log_p_%s', to_char(period_start, 'YYYYMMDD'));
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.activity_log_partitioned FOR VALUES FROM (%L) TO (%L)',
            table_name,
            period_start,
            period_start + interval '7 days'
        );
        period_start := period_start + interval '7 days';
    END LOOP;
END
$$;

CREATE TABLE public.activity_log_default
    PARTITION OF public.activity_log_partitioned DEFAULT;

INSERT INTO public.activity_log_partitioned (
    id,
    "timestamp",
    client_ip,
    user_id,
    session_id,
    method,
    path,
    status_code,
    user_agent,
    duration_ms,
    request_id,
    node_id,
    path_pattern,
    playback_session_id,
    impersonator_user_id
)
SELECT
    id,
    "timestamp",
    client_ip,
    user_id,
    session_id,
    method,
    path,
    status_code,
    user_agent,
    duration_ms,
    request_id,
    node_id,
    path_pattern,
    playback_session_id,
    impersonator_user_id
FROM public.activity_log;

SELECT setval(
    'public.activity_log_id_seq',
    COALESCE((SELECT MAX(id) FROM public.activity_log_partitioned), 1),
    EXISTS (SELECT 1 FROM public.activity_log_partitioned)
);

ALTER TABLE public.activity_log RENAME TO activity_log_old;
ALTER TABLE public.activity_log_partitioned RENAME TO activity_log;
DROP TABLE public.activity_log_old;

ALTER SEQUENCE public.activity_log_id_seq
    OWNED BY public.activity_log.id;

CREATE INDEX idx_activity_log_timestamp_id
    ON public.activity_log USING btree ("timestamp" DESC, id DESC);
CREATE INDEX idx_activity_log_client_ip
    ON public.activity_log USING btree (client_ip, "timestamp" DESC);
CREATE INDEX idx_activity_log_user_id
    ON public.activity_log USING btree (user_id, "timestamp" DESC);
CREATE INDEX idx_activity_log_playback_session_id
    ON public.activity_log USING btree (playback_session_id, "timestamp" DESC);
CREATE INDEX idx_activity_log_request_id
    ON public.activity_log USING btree (request_id, "timestamp" DESC);
CREATE INDEX idx_activity_log_impersonator_user_id
    ON public.activity_log USING btree (impersonator_user_id, "timestamp" DESC)
    WHERE impersonator_user_id IS NOT NULL;

ALTER TABLE public.activity_log
    ADD CONSTRAINT activity_log_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TABLE public.operational_logs_unpartitioned (
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
    attrs jsonb DEFAULT '{}'::jsonb NOT NULL,
    playback_session_id text
);

INSERT INTO public.operational_logs_unpartitioned (
    id,
    "timestamp",
    level,
    component,
    message,
    request_id,
    user_id,
    session_id,
    client_ip,
    node_id,
    attrs,
    playback_session_id
)
OVERRIDING SYSTEM VALUE
SELECT
    id,
    "timestamp",
    level,
    component,
    message,
    request_id,
    user_id,
    session_id,
    client_ip,
    node_id,
    attrs,
    playback_session_id
FROM public.operational_logs;

DO $$
DECLARE
    seq_name text;
    max_id bigint;
BEGIN
    SELECT pg_get_serial_sequence('public.operational_logs_unpartitioned', 'id') INTO seq_name;
    SELECT MAX(id) INTO max_id FROM public.operational_logs_unpartitioned;
    IF max_id IS NULL THEN
        EXECUTE format('SELECT setval(%L, 1, false)', seq_name);
    ELSE
        EXECUTE format('SELECT setval(%L, %s, true)', seq_name, max_id);
    END IF;
END
$$;

ALTER TABLE public.operational_logs RENAME TO operational_logs_partitioned_old;
ALTER TABLE public.operational_logs_unpartitioned RENAME TO operational_logs;
DROP TABLE public.operational_logs_partitioned_old;

CREATE INDEX idx_operational_logs_timestamp
    ON public.operational_logs USING btree ("timestamp" DESC);
CREATE INDEX idx_operational_logs_level
    ON public.operational_logs USING btree (level, "timestamp" DESC);
CREATE INDEX idx_operational_logs_component
    ON public.operational_logs USING btree (component, "timestamp" DESC);
CREATE INDEX idx_operational_logs_request_id
    ON public.operational_logs USING btree (request_id, "timestamp" DESC);
CREATE INDEX idx_operational_logs_user_id
    ON public.operational_logs USING btree (user_id, "timestamp" DESC);
CREATE INDEX idx_operational_logs_session_id
    ON public.operational_logs USING btree (session_id, "timestamp" DESC);
CREATE INDEX idx_operational_logs_playback_session_id
    ON public.operational_logs USING btree (playback_session_id, "timestamp" DESC);
CREATE INDEX idx_operational_logs_component_level
    ON public.operational_logs USING btree (component, level, "timestamp" DESC);

ALTER TABLE public.operational_logs
    ADD CONSTRAINT operational_logs_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE public.activity_log
    ALTER COLUMN id DROP DEFAULT;
ALTER SEQUENCE public.activity_log_id_seq
    OWNED BY NONE;

CREATE TABLE public.activity_log_unpartitioned (
    id bigint NOT NULL DEFAULT nextval('public.activity_log_id_seq'::regclass),
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    client_ip inet NOT NULL,
    user_id integer,
    session_id text,
    method text NOT NULL,
    path text NOT NULL,
    status_code integer,
    user_agent text,
    duration_ms integer,
    request_id text,
    node_id text,
    path_pattern text,
    playback_session_id text,
    impersonator_user_id integer,
    PRIMARY KEY (id)
);

INSERT INTO public.activity_log_unpartitioned (
    id,
    "timestamp",
    client_ip,
    user_id,
    session_id,
    method,
    path,
    status_code,
    user_agent,
    duration_ms,
    request_id,
    node_id,
    path_pattern,
    playback_session_id,
    impersonator_user_id
)
SELECT
    id,
    "timestamp",
    client_ip,
    user_id,
    session_id,
    method,
    path,
    status_code,
    user_agent,
    duration_ms,
    request_id,
    node_id,
    path_pattern,
    playback_session_id,
    impersonator_user_id
FROM public.activity_log;

SELECT setval(
    'public.activity_log_id_seq',
    COALESCE((SELECT MAX(id) FROM public.activity_log_unpartitioned), 1),
    EXISTS (SELECT 1 FROM public.activity_log_unpartitioned)
);

ALTER TABLE public.activity_log RENAME TO activity_log_partitioned_old;
ALTER TABLE public.activity_log_unpartitioned RENAME TO activity_log;
DROP TABLE public.activity_log_partitioned_old;

ALTER SEQUENCE public.activity_log_id_seq
    OWNED BY public.activity_log.id;

CREATE INDEX idx_activity_log_client_ip
    ON public.activity_log USING btree (client_ip, "timestamp" DESC);
CREATE INDEX idx_activity_log_timestamp
    ON public.activity_log USING btree ("timestamp" DESC);
CREATE INDEX idx_activity_log_user_id
    ON public.activity_log USING btree (user_id, "timestamp" DESC);
CREATE INDEX idx_activity_log_playback_session_id
    ON public.activity_log USING btree (playback_session_id, "timestamp" DESC);
CREATE INDEX idx_activity_log_request_id
    ON public.activity_log USING btree (request_id, "timestamp" DESC);
CREATE INDEX idx_activity_log_impersonator_user_id
    ON public.activity_log USING btree (impersonator_user_id, "timestamp" DESC)
    WHERE impersonator_user_id IS NOT NULL;

ALTER TABLE public.activity_log
    ADD CONSTRAINT activity_log_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;
-- +goose StatementEnd
