-- plugin_auth_identities.answer_seq orders a sign-in against the provider
-- re-checks of its identity. A re-check that records the provider's answer
-- about the account (active, or a refusal) stores the next value of one
-- global sequence with it. A sign-in takes a value before it asks the
-- provider, and its answer is not applied when the identity holds a later
-- one: the re-check may have asked after the sign-in did. A sequence is used
-- because a clock can step back.
--
-- 0 means no such answer is on record: existing links, and links a sign-in
-- creates. A link an administrator makes takes a value, so an answer obtained
-- before that link existed is not applied to it.

-- +goose Up
-- +goose StatementBegin
CREATE SEQUENCE plugin_auth_identity_answer_seq;

ALTER TABLE plugin_auth_identities
    ADD COLUMN answer_seq BIGINT NOT NULL DEFAULT 0;

ALTER SEQUENCE plugin_auth_identity_answer_seq OWNED BY plugin_auth_identities.answer_seq;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE plugin_auth_identities DROP COLUMN answer_seq;
-- +goose StatementEnd
