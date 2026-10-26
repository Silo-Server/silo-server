-- +goose Up
-- +goose StatementBegin
-- Link invitations: the admin shares the claim link themselves and the
-- invitee enters their own email address at accept, so a pending link
-- invitation has no address. Accept records the address the account took.
ALTER TABLE public.invitations ALTER COLUMN email DROP NOT NULL;

-- How the claim link reached the invitee. NULL for rows created before this
-- was recorded. email_unconfirmed is written before the send and replaced by
-- email_sent only after the mail server accepts the message.
ALTER TABLE public.invitations
    ADD COLUMN delivery text
        CONSTRAINT invitations_delivery_check
        CHECK (delivery IN ('link', 'email_sent', 'email_unconfirmed'));

ALTER TABLE public.invitations
    ADD CONSTRAINT invitations_email_or_link_check
    CHECK (email IS NOT NULL OR delivery = 'link');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Pending link invitations cannot be represented without an address.
DELETE FROM public.invitations WHERE email IS NULL;
ALTER TABLE public.invitations DROP CONSTRAINT invitations_email_or_link_check;
ALTER TABLE public.invitations DROP COLUMN delivery;
ALTER TABLE public.invitations ALTER COLUMN email SET NOT NULL;
-- +goose StatementEnd
