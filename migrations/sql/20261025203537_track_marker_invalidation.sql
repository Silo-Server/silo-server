-- +goose NO TRANSACTION

-- +goose Up
-- Records when invalidate_file_markers last cleared a file's derived markers.
-- Local detection settles a season group or a movie once and skips it while
-- what it reads stays the same, but a rematch or a new modification time
-- clears the markers it wrote without changing that. Season input signatures
-- include this time, and a movie's credits tail records the time it settled
-- against, so detection writes the markers again after an invalidation.
--
-- Each statement commits on its own, so the ALTER TABLE lock on media_files
-- does not last the whole backfill below, which takes only row locks. Every
-- statement is safe to re-run if the migration stops partway.
ALTER TABLE media_files ADD COLUMN IF NOT EXISTS markers_invalidated_at timestamptz;
ALTER TABLE media_intro_fingerprints ADD COLUMN IF NOT EXISTS markers_invalidated_at timestamptz;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION invalidate_file_markers() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    -- Per-kind provenance, with the shared column standing in only for rows that
    -- carry no per-kind provenance anywhere. Rows written before the per-kind
    -- columns existed have one source for every kind, so the shared value is
    -- theirs. On a row where at least one kind has its own source, a kind with
    -- none has unknown provenance instead: a catalog import writes bounds with no
    -- source, and those ranges can be neither fetched nor detected again.
    shared_source text;
    intro_source text;
    credits_source text;
    recap_source text;
    preview_source text;
BEGIN
    IF ROW(COALESCE(NEW.file_hash, ''), COALESCE(NEW.file_size, 0), NEW.file_modified_at,
           COALESCE(NEW.duration, 0), COALESCE(NEW.content_id, ''), COALESCE(NEW.episode_id, ''),
           COALESCE(NEW.extra_id, ''), COALESCE(NEW.season_number, 0), COALESCE(NEW.episode_number, 0))
       IS NOT DISTINCT FROM
       ROW(COALESCE(OLD.file_hash, ''), COALESCE(OLD.file_size, 0), OLD.file_modified_at,
           COALESCE(OLD.duration, 0), COALESCE(OLD.content_id, ''), COALESCE(OLD.episode_id, ''),
           COALESCE(OLD.extra_id, ''), COALESCE(OLD.season_number, 0), COALESCE(OLD.episode_number, 0)) THEN
        RETURN NEW;
    END IF;

    NEW.markers_invalidated_at := now();

    IF NEW.intro_markers_source IS NULL AND NEW.credits_markers_source IS NULL
       AND NEW.recap_markers_source IS NULL AND NEW.preview_markers_source IS NULL THEN
        shared_source := COALESCE(NEW.markers_source, 'manual');
    ELSE
        shared_source := 'manual';
    END IF;
    intro_source := COALESCE(NEW.intro_markers_source, shared_source);
    credits_source := COALESCE(NEW.credits_markers_source, shared_source);
    recap_source := COALESCE(NEW.recap_markers_source, shared_source);
    preview_source := COALESCE(NEW.preview_markers_source, shared_source);

    SELECT COALESCE(jsonb_agg(segment), '[]'::jsonb) INTO NEW.marker_segments
    FROM jsonb_array_elements(NEW.marker_segments) AS segment
    WHERE CASE segment->>'kind'
        WHEN 'intro' THEN intro_source = 'manual'
        WHEN 'credits' THEN credits_source = 'manual'
        WHEN 'recap' THEN recap_source = 'manual'
        WHEN 'preview' THEN preview_source = 'manual'
        ELSE false
    END;

    IF intro_source <> 'manual' THEN
        NEW.intro_start := NULL;
        NEW.intro_end := NULL;
        NEW.intro_markers_source := NULL;
        NEW.intro_markers_provider := NULL;
        NEW.intro_markers_confidence := NULL;
        NEW.intro_markers_algorithm := NULL;
        NEW.intro_markers_detected_at := NULL;
    END IF;

    IF credits_source <> 'manual' THEN
        NEW.credits_start := NULL;
        NEW.credits_end := NULL;
        NEW.credits_markers_source := NULL;
        NEW.credits_markers_provider := NULL;
        NEW.credits_markers_confidence := NULL;
        NEW.credits_markers_algorithm := NULL;
        NEW.credits_markers_detected_at := NULL;
    END IF;

    IF recap_source <> 'manual' THEN
        NEW.recap_start := NULL;
        NEW.recap_end := NULL;
        NEW.recap_markers_source := NULL;
        NEW.recap_markers_provider := NULL;
        NEW.recap_markers_confidence := NULL;
        NEW.recap_markers_algorithm := NULL;
        NEW.recap_markers_detected_at := NULL;
    END IF;

    IF preview_source <> 'manual' THEN
        NEW.preview_start := NULL;
        NEW.preview_end := NULL;
        NEW.preview_markers_source := NULL;
        NEW.preview_markers_provider := NULL;
        NEW.preview_markers_confidence := NULL;
        NEW.preview_markers_algorithm := NULL;
        NEW.preview_markers_detected_at := NULL;
    END IF;

    IF NEW.intro_start IS NOT NULL OR NEW.credits_start IS NOT NULL
       OR NEW.recap_start IS NOT NULL OR NEW.preview_start IS NOT NULL THEN
        NEW.markers_source := 'manual';
        -- Only the surviving per-kind confidences count. Folding the previous
        -- shared value in kept a confidence belonging to a range this trigger
        -- just deleted (GREATEST ignores NULLs, so the old value survived every
        -- identity change even when nothing left carried one). This mirrors
        -- recomputeSharedMarkerAttribution in internal/scanner/file_repo.go,
        -- which derives the same columns from surviving segments only and leaves
        -- them NULL when none reports one.
        NEW.markers_confidence := GREATEST(NEW.intro_markers_confidence, NEW.credits_markers_confidence,
                                          NEW.recap_markers_confidence, NEW.preview_markers_confidence);
    ELSE
        NEW.markers_source := NULL;
        NEW.markers_confidence := NULL;
    END IF;
    DELETE FROM marker_fetch_state WHERE media_file_id = NEW.id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Nothing records which files lost markers to that before this migration, so
-- every file missing a range local detection places is stamped once: an
-- episode without an intro or credits, and a movie without credits. The next
-- scheduled run looks at those seasons again over their cached fingerprints,
-- and at those movies again from their cached tails, reading audio for a
-- silence only where a tail places credits. Movies hold no detected intro, so
-- a movie with credits stays settled.
UPDATE media_files SET markers_invalidated_at = now()
WHERE markers_invalidated_at IS NULL
  AND COALESCE(extra_id, '') = ''
  AND (credits_start IS NULL OR (episode_id IS NOT NULL AND intro_start IS NULL));

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION invalidate_file_markers() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    -- Per-kind provenance, with the shared column standing in only for rows that
    -- carry no per-kind provenance anywhere. Rows written before the per-kind
    -- columns existed have one source for every kind, so the shared value is
    -- theirs. On a row where at least one kind has its own source, a kind with
    -- none has unknown provenance instead: a catalog import writes bounds with no
    -- source, and those ranges can be neither fetched nor detected again.
    shared_source text;
    intro_source text;
    credits_source text;
    recap_source text;
    preview_source text;
BEGIN
    IF ROW(COALESCE(NEW.file_hash, ''), COALESCE(NEW.file_size, 0), NEW.file_modified_at,
           COALESCE(NEW.duration, 0), COALESCE(NEW.content_id, ''), COALESCE(NEW.episode_id, ''),
           COALESCE(NEW.extra_id, ''), COALESCE(NEW.season_number, 0), COALESCE(NEW.episode_number, 0))
       IS NOT DISTINCT FROM
       ROW(COALESCE(OLD.file_hash, ''), COALESCE(OLD.file_size, 0), OLD.file_modified_at,
           COALESCE(OLD.duration, 0), COALESCE(OLD.content_id, ''), COALESCE(OLD.episode_id, ''),
           COALESCE(OLD.extra_id, ''), COALESCE(OLD.season_number, 0), COALESCE(OLD.episode_number, 0)) THEN
        RETURN NEW;
    END IF;

    IF NEW.intro_markers_source IS NULL AND NEW.credits_markers_source IS NULL
       AND NEW.recap_markers_source IS NULL AND NEW.preview_markers_source IS NULL THEN
        shared_source := COALESCE(NEW.markers_source, 'manual');
    ELSE
        shared_source := 'manual';
    END IF;
    intro_source := COALESCE(NEW.intro_markers_source, shared_source);
    credits_source := COALESCE(NEW.credits_markers_source, shared_source);
    recap_source := COALESCE(NEW.recap_markers_source, shared_source);
    preview_source := COALESCE(NEW.preview_markers_source, shared_source);

    SELECT COALESCE(jsonb_agg(segment), '[]'::jsonb) INTO NEW.marker_segments
    FROM jsonb_array_elements(NEW.marker_segments) AS segment
    WHERE CASE segment->>'kind'
        WHEN 'intro' THEN intro_source = 'manual'
        WHEN 'credits' THEN credits_source = 'manual'
        WHEN 'recap' THEN recap_source = 'manual'
        WHEN 'preview' THEN preview_source = 'manual'
        ELSE false
    END;

    IF intro_source <> 'manual' THEN
        NEW.intro_start := NULL;
        NEW.intro_end := NULL;
        NEW.intro_markers_source := NULL;
        NEW.intro_markers_provider := NULL;
        NEW.intro_markers_confidence := NULL;
        NEW.intro_markers_algorithm := NULL;
        NEW.intro_markers_detected_at := NULL;
    END IF;

    IF credits_source <> 'manual' THEN
        NEW.credits_start := NULL;
        NEW.credits_end := NULL;
        NEW.credits_markers_source := NULL;
        NEW.credits_markers_provider := NULL;
        NEW.credits_markers_confidence := NULL;
        NEW.credits_markers_algorithm := NULL;
        NEW.credits_markers_detected_at := NULL;
    END IF;

    IF recap_source <> 'manual' THEN
        NEW.recap_start := NULL;
        NEW.recap_end := NULL;
        NEW.recap_markers_source := NULL;
        NEW.recap_markers_provider := NULL;
        NEW.recap_markers_confidence := NULL;
        NEW.recap_markers_algorithm := NULL;
        NEW.recap_markers_detected_at := NULL;
    END IF;

    IF preview_source <> 'manual' THEN
        NEW.preview_start := NULL;
        NEW.preview_end := NULL;
        NEW.preview_markers_source := NULL;
        NEW.preview_markers_provider := NULL;
        NEW.preview_markers_confidence := NULL;
        NEW.preview_markers_algorithm := NULL;
        NEW.preview_markers_detected_at := NULL;
    END IF;

    IF NEW.intro_start IS NOT NULL OR NEW.credits_start IS NOT NULL
       OR NEW.recap_start IS NOT NULL OR NEW.preview_start IS NOT NULL THEN
        NEW.markers_source := 'manual';
        -- Only the surviving per-kind confidences count. Folding the previous
        -- shared value in kept a confidence belonging to a range this trigger
        -- just deleted (GREATEST ignores NULLs, so the old value survived every
        -- identity change even when nothing left carried one). This mirrors
        -- recomputeSharedMarkerAttribution in internal/scanner/file_repo.go,
        -- which derives the same columns from surviving segments only and leaves
        -- them NULL when none reports one.
        NEW.markers_confidence := GREATEST(NEW.intro_markers_confidence, NEW.credits_markers_confidence,
                                          NEW.recap_markers_confidence, NEW.preview_markers_confidence);
    ELSE
        NEW.markers_source := NULL;
        NEW.markers_confidence := NULL;
    END IF;
    DELETE FROM marker_fetch_state WHERE media_file_id = NEW.id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE media_intro_fingerprints DROP COLUMN IF EXISTS markers_invalidated_at;
ALTER TABLE media_files DROP COLUMN IF EXISTS markers_invalidated_at;
