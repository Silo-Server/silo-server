-- +goose NO TRANSACTION
-- +goose Up
-- An administrator test push now carries the target profile's newest
-- notification, so a test attempt names a real delivery and can share its
-- (device, delivery, attempt number) with that delivery's own attempt and with
-- earlier tests. The at-most-once key only ever guarded fanout's enqueue, so
-- scope it to delivery attempts.
--
-- The new key is built before the old one is dropped, so fanout is never
-- without it. A failed nontransactional run can leave the new index invalid;
-- drop it concurrently before rebuilding so a retry recovers.
DROP INDEX CONCURRENTLY IF EXISTS public.push_delivery_attempts_delivery_trigger_unique;
CREATE UNIQUE INDEX CONCURRENTLY push_delivery_attempts_delivery_trigger_unique
    ON public.push_delivery_attempts (push_device_id, notification_delivery_id, attempt_number)
    WHERE trigger_type = 'delivery';
DROP INDEX CONCURRENTLY IF EXISTS public.push_delivery_attempts_delivery_unique;

-- +goose Down
-- The wider key cannot hold test attempts beside a delivery's own, so detach
-- them from their deliveries first; their history rows remain.
UPDATE public.push_delivery_attempts
SET notification_delivery_id = NULL
WHERE trigger_type = 'test' AND notification_delivery_id IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS public.push_delivery_attempts_delivery_unique;
CREATE UNIQUE INDEX CONCURRENTLY push_delivery_attempts_delivery_unique
    ON public.push_delivery_attempts (push_device_id, notification_delivery_id, attempt_number)
    WHERE notification_delivery_id IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS public.push_delivery_attempts_delivery_trigger_unique;
