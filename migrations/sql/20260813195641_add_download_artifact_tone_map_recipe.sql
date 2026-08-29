-- +goose Up
ALTER TABLE public.download_artifacts
    ADD COLUMN tone_map_policy text NOT NULL DEFAULT 'none',
    ADD COLUMN tone_map_mode text NOT NULL DEFAULT '',
    ADD COLUMN tone_map_source_kind text NOT NULL DEFAULT '',
    ADD COLUMN tone_map_recipe_version text NOT NULL DEFAULT '',
    ADD COLUMN tone_map_preflight_required boolean NOT NULL DEFAULT false,
    ADD COLUMN tone_map_source_revision text NOT NULL DEFAULT '',
    ADD COLUMN tone_map_dv_config_present boolean NOT NULL DEFAULT false,
    ADD COLUMN tone_map_dv_bl_compat_id_present boolean NOT NULL DEFAULT false,
    ADD COLUMN tone_map_dv_bl_present boolean NOT NULL DEFAULT false,
    ADD COLUMN tone_map_dv_rpu_present boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT download_artifacts_tone_map_recipe_check CHECK (
        (tone_map_policy = 'none' AND tone_map_mode = '' AND tone_map_source_kind = '' AND tone_map_recipe_version = '' AND tone_map_preflight_required = false AND tone_map_source_revision = '' AND tone_map_dv_config_present = false AND tone_map_dv_bl_compat_id_present = false AND tone_map_dv_bl_present = false AND tone_map_dv_rpu_present = false)
        OR
        ((((tone_map_policy = 'hardware_only' OR tone_map_policy = 'hardware_then_software') AND tone_map_mode = 'hardware')
            OR ((tone_map_policy = 'software_only' OR tone_map_policy = 'hardware_then_software') AND tone_map_mode = 'software'))
            AND tone_map_source_kind IN ('pq', 'hlg', 'hlg_bt709', 'sdr_bt709', 'sdr_bt2020')
            AND tone_map_recipe_version = '1'
            AND tone_map_source_revision <> '')
    ) NOT VALID;

-- +goose Down
ALTER TABLE public.download_artifacts
    DROP CONSTRAINT IF EXISTS download_artifacts_tone_map_recipe_check,
    DROP COLUMN IF EXISTS tone_map_recipe_version,
    DROP COLUMN IF EXISTS tone_map_source_revision,
    DROP COLUMN IF EXISTS tone_map_preflight_required,
    DROP COLUMN IF EXISTS tone_map_dv_rpu_present,
    DROP COLUMN IF EXISTS tone_map_dv_bl_present,
    DROP COLUMN IF EXISTS tone_map_dv_bl_compat_id_present,
    DROP COLUMN IF EXISTS tone_map_dv_config_present,
    DROP COLUMN IF EXISTS tone_map_source_kind,
    DROP COLUMN IF EXISTS tone_map_mode,
    DROP COLUMN IF EXISTS tone_map_policy;
