-- +goose Up
CREATE SEQUENCE public.notification_webhook_revision_seq AS bigint NO CYCLE;
ALTER TABLE public.notification_webhooks ADD COLUMN revision bigint NOT NULL DEFAULT nextval('public.notification_webhook_revision_seq');
-- +goose StatementBegin
CREATE FUNCTION public.assign_notification_webhook_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.revision := nextval('public.notification_webhook_revision_seq');
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notification_webhook_revision BEFORE INSERT OR UPDATE ON public.notification_webhooks
FOR EACH ROW EXECUTE FUNCTION public.assign_notification_webhook_revision();

-- +goose Down
DROP TRIGGER notification_webhook_revision ON public.notification_webhooks;
DROP FUNCTION public.assign_notification_webhook_revision();
ALTER TABLE public.notification_webhooks DROP COLUMN revision;
DROP SEQUENCE public.notification_webhook_revision_seq;
