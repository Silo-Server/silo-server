-- Audit log of marker contributions to online providers. The content_hash over
-- the contributed value and resolved provider target gives idempotency: the same
-- segment value is never resubmitted to the same provider+target, but a
-- corrected value or rematch (different hash) is.
CREATE TABLE public.marker_contributions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    media_file_id      integer NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    provider           text    NOT NULL,
    segment_kind       text    NOT NULL,                -- intro | recap | credits | preview
    source             text    NOT NULL,                -- what we contributed: scanner | manual
    submitted_start_ms bigint,
    submitted_end_ms   bigint,
    video_duration_ms  bigint,
    content_hash       text    NOT NULL,                -- hash(segment_kind, bounds, duration, resolved target)
    submission_id      text,                            -- id returned by the provider
    status             text    NOT NULL,                -- pending | accepted | rejected | error
    http_status        integer,
    error              text,
    submitted_at       timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (media_file_id, provider, segment_kind, content_hash)
);

CREATE INDEX marker_contributions_file_idx ON public.marker_contributions (media_file_id);
