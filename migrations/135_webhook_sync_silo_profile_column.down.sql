DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'webhook_sync_actor_mappings'
          AND column_name = 'silo_profile_id'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'webhook_sync_actor_mappings'
          AND column_name = 'continuum_profile_id'
    ) THEN
        ALTER TABLE public.webhook_sync_actor_mappings
            RENAME COLUMN silo_profile_id TO continuum_profile_id;
    ELSIF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'webhook_sync_actor_mappings'
          AND column_name = 'silo_profile_id'
    ) THEN
        UPDATE public.webhook_sync_actor_mappings
        SET continuum_profile_id = COALESCE(continuum_profile_id, silo_profile_id)
        WHERE continuum_profile_id IS NULL
          AND silo_profile_id IS NOT NULL;
    END IF;
END $$;
