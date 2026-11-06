-- +goose Up
-- A plugin destination belongs to its account, like a webhook
-- (20260912231023_user_fk_integrity): deleting the account deletes its
-- destinations, and their delivery attempts with them. Destinations left by
-- accounts deleted before this constraint are removed first.
DELETE FROM public.notification_plugin_destinations destination
WHERE NOT EXISTS (SELECT 1 FROM public.users account WHERE account.id = destination.user_id);

ALTER TABLE public.notification_plugin_destinations
    ADD CONSTRAINT notification_plugin_destinations_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE public.notification_plugin_destinations
    DROP CONSTRAINT notification_plugin_destinations_user_id_fkey;
