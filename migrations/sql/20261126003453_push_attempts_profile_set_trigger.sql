-- +goose Up
-- +goose StatementBegin
-- A profile_set attempt is a background wake telling an account's phones
-- that a profile from their profile set changed on this server. Like an
-- administrator's test push it names no notification delivery.
ALTER TABLE public.push_delivery_attempts
    DROP CONSTRAINT push_delivery_attempts_trigger_check,
    DROP CONSTRAINT push_delivery_attempts_delivery_required_check,
    ADD CONSTRAINT push_delivery_attempts_trigger_check
        CHECK (trigger_type IN ('delivery', 'test', 'profile_set')),
    ADD CONSTRAINT push_delivery_attempts_delivery_required_check
        CHECK (trigger_type <> 'delivery' OR notification_delivery_id IS NOT NULL);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM public.push_delivery_attempts WHERE trigger_type = 'profile_set';
ALTER TABLE public.push_delivery_attempts
    DROP CONSTRAINT push_delivery_attempts_trigger_check,
    DROP CONSTRAINT push_delivery_attempts_delivery_required_check,
    ADD CONSTRAINT push_delivery_attempts_trigger_check CHECK (trigger_type IN ('delivery', 'test')),
    ADD CONSTRAINT push_delivery_attempts_delivery_required_check CHECK (
        (trigger_type = 'delivery' AND notification_delivery_id IS NOT NULL)
        OR trigger_type = 'test'
    );
-- +goose StatementEnd
