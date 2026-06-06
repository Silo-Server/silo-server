ALTER TABLE public.autoscan_sources
    ADD COLUMN source_config jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Move CephFS source-specific settings out of generic plugin runtime config
-- when upgrading dev/early installs. The plugin may still keep runtime-only
-- state settings there, but watch roots and exclusions belong to autoscan
-- source rows so operators manage them from Admin > Autoscan.
UPDATE public.autoscan_sources s
SET source_config = jsonb_strip_nulls(jsonb_build_object(
    'movie_flat_paths', cfg.config_value->>'movie_flat_paths',
    'tv_flat_paths', cfg.config_value->>'tv_flat_paths',
    'movie_nested_paths', cfg.config_value->>'movie_nested_paths',
    'tv_nested_paths', cfg.config_value->>'tv_nested_paths',
    'exclusions', cfg.config_value->>'exclusions'
))
FROM public.plugin_installations pi
JOIN public.plugin_runtime_configs cfg
    ON cfg.plugin_installation_id = pi.id
   AND cfg.config_key = 'cephfs_monitor'
WHERE s.installation_id = pi.id
  AND pi.plugin_id = 'silo.autoscan.cephfs'
  AND s.capability_id = 'cephfs'
  AND s.source_config = '{}'::jsonb;
