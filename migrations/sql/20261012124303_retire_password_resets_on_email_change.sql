-- +goose Up
-- Recovery identity changes retire existing capabilities in the same transaction,
-- including writes outside the account administration APIs.
-- +goose StatementBegin
CREATE FUNCTION public.retire_password_resets_on_email_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    DELETE FROM public.password_reset_tokens WHERE user_id = NEW.id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER retire_password_resets_on_email_change
AFTER UPDATE OF email ON public.users
FOR EACH ROW WHEN (OLD.email IS DISTINCT FROM NEW.email)
EXECUTE FUNCTION public.retire_password_resets_on_email_change();

-- +goose Down
DROP TRIGGER retire_password_resets_on_email_change ON public.users;
DROP FUNCTION public.retire_password_resets_on_email_change();
