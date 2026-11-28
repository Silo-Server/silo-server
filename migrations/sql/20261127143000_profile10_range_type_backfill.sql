-- +goose Up
-- Dolby Vision profile 10 (AV1) tracks scanned before #4487 were stored with
-- video_range_type DOVI even when their configuration record names a
-- compatible base layer. New scans store the range type
-- mediaprobe.Profile10RangeType gives (10.1 HDR10, or HDR10+ with
-- hdr10_plus; 10.2 SDR; 10.4 HLG), and the Jellyfin-compatible API corrects
-- old rows on read, but the native API returned the stored DOVI until the
-- file changed. This rewrites those stored tracks the same way. A track with
-- no compatibility id, or one Jellyfin does not map, stays DOVI, as a scan
-- leaves it. Every other track, and its order, is kept as it is.
UPDATE media_files AS mf
SET video_tracks = fixed.tracks
FROM (
    SELECT f.id,
           jsonb_agg(
               CASE
                   WHEN track ->> 'dv_profile' = '10'
                        AND lower(track ->> 'video_range_type') = 'dovi'
                        AND track ->> 'dv_bl_compat_id' IN ('1', '2', '4')
                   THEN jsonb_set(track, '{video_range_type}', to_jsonb(
                       CASE track ->> 'dv_bl_compat_id'
                           WHEN '1' THEN CASE WHEN track ->> 'hdr10_plus' = 'true'
                                              THEN 'DOVIWithHDR10Plus' ELSE 'DOVIWithHDR10' END
                           WHEN '2' THEN 'DOVIWithSDR'
                           ELSE 'DOVIWithHLG'
                       END))
                   ELSE track
               END
               ORDER BY position
           ) AS tracks
    FROM media_files AS f
    CROSS JOIN LATERAL jsonb_array_elements(f.video_tracks) WITH ORDINALITY AS t(track, position)
    WHERE jsonb_typeof(f.video_tracks) = 'array'
      AND f.video_tracks @? '$[*] ? (@.dv_profile == 10 && @.video_range_type like_regex "^dovi$" flag "i" && (@.dv_bl_compat_id == 1 || @.dv_bl_compat_id == 2 || @.dv_bl_compat_id == 4))'
    GROUP BY f.id
) AS fixed
WHERE mf.id = fixed.id;

-- +goose Down
-- The rewritten range types are what a scan of the same files stores now, and
-- the old DOVI value can't be told apart from a genuine one, so nothing is
-- put back.
SELECT 1;
