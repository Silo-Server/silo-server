CREATE TABLE public.autoscan_events (
    id BIGSERIAL PRIMARY KEY,
    source_id uuid REFERENCES public.autoscan_sources(id) ON DELETE SET NULL,
    installation_id integer NOT NULL,
    capability_id text NOT NULL,
    started_at timestamptz NOT NULL,
    completed_at timestamptz NOT NULL,
    duration_ms bigint NOT NULL DEFAULT 0,
    status text NOT NULL,
    changes_returned integer NOT NULL DEFAULT 0,
    changes_resolved integer NOT NULL DEFAULT 0,
    targets_claimed integer NOT NULL DEFAULT 0,
    scans_created integer NOT NULL DEFAULT 0,
    scans_reused integer NOT NULL DEFAULT 0,
    scans_suppressed integer NOT NULL DEFAULT 0,
    error_message text NOT NULL DEFAULT '',
    marker_before text,
    marker_after text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT autoscan_events_status_check CHECK (status = ANY (ARRAY['success'::text, 'error'::text, 'unresolved'::text]))
);

CREATE INDEX idx_autoscan_events_source_completed
    ON public.autoscan_events (source_id, completed_at DESC);

CREATE INDEX idx_autoscan_events_completed
    ON public.autoscan_events (completed_at DESC);

ALTER TABLE public.scan_runs
    ADD COLUMN autoscan_event_id bigint REFERENCES public.autoscan_events(id) ON DELETE SET NULL;

CREATE INDEX idx_scan_runs_autoscan_event
    ON public.scan_runs (autoscan_event_id);
