-- +goose Up
-- The last transaction that signed each account out everywhere. A password
-- sign-in snapshots the database before it checks the credentials and,
-- under the account lock, refuses to open a session when the snapshot
-- cannot see this transaction: the sign-out committed after the check
-- began, so its revocation missed the session the sign-in is about to open.
-- See docs/architecture/external-sign-in.md.
CREATE TABLE account_sign_outs (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    xid XID8 NOT NULL
);

-- +goose Down
DROP TABLE account_sign_outs;
