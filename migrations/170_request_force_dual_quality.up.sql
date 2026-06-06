ALTER TABLE public.request_settings
    ADD COLUMN IF NOT EXISTS force_dual_quality boolean NOT NULL DEFAULT false;
