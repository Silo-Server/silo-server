DROP TABLE IF EXISTS public.marker_edit_audit;

UPDATE public.users
SET permissions = array_remove(permissions, 'marker_edit')
WHERE role <> 'admin';

ALTER TABLE public.users
    ALTER COLUMN permissions SET DEFAULT '{}'::text[];
