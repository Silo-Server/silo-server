-- +goose Up
-- Trickplay sheets now convert HDR with the chains playback validated for the
-- source's base signal, and repair cells a list read missed or repeated (see
-- docs/architecture/trickplay.md). Regenerate the published sheets the old
-- way got wrong, while they keep serving until their replacement publishes:
-- HDR sheets the VAAPI or QSV tone mapper made, which rendered a Dolby Vision
-- base without its RPU (green and magenta) and HLG as PQ, and sheets with
-- cells filled from a neighbor. Files whose last attempts failed, such as
-- VC-1 in Matroska, which flags no keyframes, are tried again now instead of
-- after their backoff.
UPDATE public.media_file_trickplay t
SET state = 'pending', available_at = now(), failure_count = 0, last_error = '', updated_at = now()
FROM public.media_files mf
WHERE mf.id = t.media_file_id
  AND t.state = 'ready'
  AND (
    (t.decoder IN ('hardware:vaapi', 'hardware:qsv')
      AND (mf.hdr OR btrim(COALESCE(mf.video_tracks -> 0 ->> 'dolby_vision', '')) <> ''))
    OR COALESCE(t.filled, 0) > 0
  );

UPDATE public.media_file_trickplay
SET available_at = now(), updated_at = now()
WHERE state = 'pending' AND failure_count > 0 AND available_at > now();

-- +goose Down
-- Requeued rows regenerate or keep serving their sheets; there is nothing to
-- restore.
SELECT 1;
