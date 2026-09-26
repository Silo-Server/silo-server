-- +goose Up
CREATE INDEX invitations_created_id_idx ON invitations (created_at DESC, id DESC);

-- +goose Down
DROP INDEX invitations_created_id_idx;
