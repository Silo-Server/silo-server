-- +goose NO TRANSACTION

-- +goose Up
-- Tone-mapped downloads of Dolby Vision Profile 5 and PQ-tagged compatibility
-- id 0 sources freeze the dolby_vision source kind.
ALTER TABLE public.download_artifacts
    DROP CONSTRAINT download_artifacts_tone_map_recipe_check,
    ADD CONSTRAINT download_artifacts_tone_map_recipe_check CHECK (
        (tone_map_policy = 'none' AND tone_map_mode = '' AND tone_map_source_kind = '' AND tone_map_recipe_version = '' AND tone_map_preflight_required = false AND tone_map_source_revision = '' AND tone_map_dv_config_present = false AND tone_map_dv_bl_compat_id_present = false AND tone_map_dv_bl_present = false AND tone_map_dv_rpu_present = false)
        OR
        ((((tone_map_policy = 'hardware_only' OR tone_map_policy = 'hardware_then_software') AND tone_map_mode = 'hardware')
            OR ((tone_map_policy = 'software_only' OR tone_map_policy = 'hardware_then_software') AND tone_map_mode = 'software'))
            AND tone_map_source_kind IN ('pq', 'hlg', 'hlg_bt709', 'sdr_bt709', 'sdr_bt2020', 'dolby_vision')
            AND tone_map_recipe_version = '1'
            AND tone_map_source_revision <> '')
    ) NOT VALID;
ALTER TABLE public.download_artifacts VALIDATE CONSTRAINT download_artifacts_tone_map_recipe_check;

-- +goose Down
-- The wider constraint is kept. An older binary never writes dolby_vision,
-- and narrowing it again would make every later update of a row that froze
-- that kind fail.
SELECT 1;
