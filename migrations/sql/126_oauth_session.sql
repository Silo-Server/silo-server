-- +goose Up
-- +goose StatementBegin
CREATE TABLE oauth_session (
  state              TEXT PRIMARY KEY,
  install_id         TEXT NOT NULL,
  redirect_uri       TEXT NOT NULL,
  linking_user_id    TEXT,
  provider_state     JSONB NOT NULL DEFAULT '{}'::jsonb,
  next_url           TEXT NOT NULL DEFAULT '/',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at         TIMESTAMPTZ NOT NULL
);

CREATE INDEX oauth_session_expires_idx ON oauth_session (expires_at);

CREATE TABLE oauth_completion (
  code_hash        TEXT PRIMARY KEY,
  token_ciphertext TEXT NOT NULL,
  expires_in       INTEGER NOT NULL,
  next_url         TEXT NOT NULL DEFAULT '/',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX oauth_completion_expires_idx ON oauth_completion (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS oauth_completion;
DROP TABLE IF EXISTS oauth_session;
-- +goose StatementEnd
