-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE public.silo_migrate_20260814191925_dolby_vision_probes()
LANGUAGE plpgsql
AS $$
DECLARE
    last_id bigint := 0;
    batch_max_id bigint;
BEGIN
    LOOP
        SELECT max(candidate.id)
        INTO batch_max_id
        FROM (
            SELECT mf.id
            FROM public.media_files AS mf
            WHERE mf.id > last_id
              AND mf.probe_updated_at IS NOT NULL
            ORDER BY mf.id
            LIMIT 500
        ) AS candidate;

        EXIT WHEN batch_max_id IS NULL;

        UPDATE public.media_files AS mf
        SET probe_updated_at = NULL
        WHERE mf.id > last_id
          AND mf.id <= batch_max_id
          AND mf.probe_updated_at IS NOT NULL
          AND EXISTS (
              SELECT 1
              FROM jsonb_array_elements(
                  CASE jsonb_typeof(mf.video_tracks)
                      WHEN 'array' THEN mf.video_tracks
                      ELSE '[]'::jsonb
                  END
              ) AS track
              WHERE (
                  track ? 'dv_profile'
                  OR lower(COALESCE(track ->> 'video_range_type', '')) LIKE '%dovi%'
                  OR lower(COALESCE(track ->> 'dolby_vision', '')) LIKE '%dolby%'
              )
                AND (
                    NOT (track ? 'dv_config_present')
                    OR NOT (track ? 'dv_bl_compat_id_present')
                )
          );

        last_id := batch_max_id;
        COMMIT;
    END LOOP;
END;
$$;
-- +goose StatementEnd

CALL public.silo_migrate_20260814191925_dolby_vision_probes();
DROP PROCEDURE public.silo_migrate_20260814191925_dolby_vision_probes();

-- +goose Down
SELECT 1;
