-- +goose Up
-- +goose StatementBegin
-- Retained domain intents prevent old HTTP retries from creating fresh links.
-- The full message (including the bearer link) is encrypted and bound to id.
CREATE TABLE public.notification_email_verifications (
    id uuid PRIMARY KEY,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    address_hash text NOT NULL,
    pending_token_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    payload_ciphertext text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_email_verifications_profile_idx
    ON public.notification_email_verifications(profile_id);
-- +goose StatementEnd

-- +goose Down
DROP TABLE public.notification_email_verifications;
