-- +goose Up
ALTER TABLE media_files ADD COLUMN marker_segments JSONB NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(marker_segments) = 'array');

CREATE TABLE marker_fetch_state (
    media_file_id BIGINT NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    provider_revision TEXT NOT NULL DEFAULT '',
    identity_key TEXT NOT NULL,
    fetched_at TIMESTAMPTZ,
    retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    failures INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    outcome TEXT NOT NULL DEFAULT 'pending',
    result JSONB,
    PRIMARY KEY (media_file_id, provider)
);
CREATE INDEX marker_fetch_state_retry_idx ON marker_fetch_state (retry_at, media_file_id);

CREATE TABLE marker_provider_cooldowns (
    provider TEXT NOT NULL,
    provider_revision TEXT NOT NULL DEFAULT '',
    retry_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (provider, provider_revision)
);

-- Seed the new option everywhere, retaining existing marker mode choices.
INSERT INTO server_settings (key, value) VALUES ('markers.online_storage', 'stored')
ON CONFLICT (key) DO NOTHING;
UPDATE server_settings SET value = CASE key WHEN 'markers.mode' THEN 'online' ELSE 'true' END
WHERE key IN ('markers.mode', 'markers.lazy_playback') AND NOT EXISTS (SELECT 1 FROM users);

-- Every file writer shares this invalidation rule, including rematching and
-- probe repairs. Explicit manual ranges survive; derived ranges (anything with
-- a known scanner, s3, online, or plugin source) belong to the previous file
-- identity and must be fetched or detected again.
--
-- A range with no provenance at all also survives. Catalog imports write
-- intro/credits bounds without a source, nothing can re-derive them, and the
-- scanner's first pass after an import stamps file_modified_at for the first
-- time, which is an identity change this trigger sees. Reading "no source" as
-- derived deleted those markers permanently on that first rescan. The 'manual'
-- COALESCE fallbacks below are what encode the rule, so they have to stay in
-- step with each other.
-- +goose StatementBegin
CREATE FUNCTION invalidate_file_markers() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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

    SELECT COALESCE(jsonb_agg(segment), '[]'::jsonb) INTO NEW.marker_segments
    FROM jsonb_array_elements(NEW.marker_segments) AS segment
    WHERE CASE segment->>'kind'
        WHEN 'intro' THEN COALESCE(NEW.intro_markers_source, NEW.markers_source, 'manual') = 'manual'
        WHEN 'credits' THEN COALESCE(NEW.credits_markers_source, NEW.markers_source, 'manual') = 'manual'
        WHEN 'recap' THEN COALESCE(NEW.recap_markers_source, NEW.markers_source, 'manual') = 'manual'
        WHEN 'preview' THEN COALESCE(NEW.preview_markers_source, NEW.markers_source, 'manual') = 'manual'
        ELSE false
    END;

    IF COALESCE(NEW.intro_markers_source, NEW.markers_source, 'manual') <> 'manual' THEN
        NEW.intro_start := NULL;
        NEW.intro_end := NULL;
        NEW.intro_markers_source := NULL;
        NEW.intro_markers_provider := NULL;
        NEW.intro_markers_confidence := NULL;
        NEW.intro_markers_algorithm := NULL;
        NEW.intro_markers_detected_at := NULL;
    END IF;

    IF COALESCE(NEW.credits_markers_source, NEW.markers_source, 'manual') <> 'manual' THEN
        NEW.credits_start := NULL;
        NEW.credits_end := NULL;
        NEW.credits_markers_source := NULL;
        NEW.credits_markers_provider := NULL;
        NEW.credits_markers_confidence := NULL;
        NEW.credits_markers_algorithm := NULL;
        NEW.credits_markers_detected_at := NULL;
    END IF;

    IF COALESCE(NEW.recap_markers_source, NEW.markers_source, 'manual') <> 'manual' THEN
        NEW.recap_start := NULL;
        NEW.recap_end := NULL;
        NEW.recap_markers_source := NULL;
        NEW.recap_markers_provider := NULL;
        NEW.recap_markers_confidence := NULL;
        NEW.recap_markers_algorithm := NULL;
        NEW.recap_markers_detected_at := NULL;
    END IF;

    IF COALESCE(NEW.preview_markers_source, NEW.markers_source, 'manual') <> 'manual' THEN
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
        NEW.markers_confidence := GREATEST(NEW.intro_markers_confidence, NEW.credits_markers_confidence,
                                          NEW.recap_markers_confidence, NEW.preview_markers_confidence,
                                          NEW.markers_confidence);
    ELSE
        NEW.markers_source := NULL;
        NEW.markers_confidence := NULL;
    END IF;
    DELETE FROM marker_fetch_state WHERE media_file_id = NEW.id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER media_files_invalidate_markers
    BEFORE UPDATE OF file_hash, file_size, file_modified_at, duration, content_id, episode_id,
                     extra_id, season_number, episode_number ON media_files
    FOR EACH ROW EXECUTE FUNCTION invalidate_file_markers();

-- +goose Down
DELETE FROM server_settings WHERE key = 'markers.online_storage';
DROP TRIGGER media_files_invalidate_markers ON media_files;
DROP FUNCTION invalidate_file_markers();
DROP TABLE marker_provider_cooldowns;
DROP TABLE marker_fetch_state;
ALTER TABLE media_files DROP COLUMN marker_segments;
