-- +goose Up
-- Request approval and quota per access group, between an account's own
-- override and the server-wide settings. Blocking is not one of its modes:
-- the group's requests_allowed switch is the one way to block a group.
CREATE TABLE request_group_limits (
    group_id bigint PRIMARY KEY REFERENCES access_groups(id) ON DELETE CASCADE,
    limit_mode text NOT NULL DEFAULT 'inherit'
        CONSTRAINT request_group_limits_limit_mode_check CHECK (limit_mode IN ('inherit', 'custom', 'unlimited')),
    max_requests integer
        CONSTRAINT request_group_limits_max_nonnegative CHECK (max_requests IS NULL OR max_requests >= 0),
    window_days integer
        CONSTRAINT request_group_limits_window_positive CHECK (window_days IS NULL OR window_days > 0),
    approval_mode text NOT NULL DEFAULT 'inherit'
        CONSTRAINT request_group_limits_approval_mode_check CHECK (approval_mode IN ('inherit', 'manual', 'auto')),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    revision bigint NOT NULL DEFAULT nextval('request_editor_revision_seq')
);
CREATE TRIGGER request_group_limits_revision BEFORE INSERT OR UPDATE ON request_group_limits
    FOR EACH ROW EXECUTE FUNCTION advance_request_editor_revision();

-- An account blocked through its request limits is blocked through its
-- requests switch instead, the one per-account way to block it.
UPDATE users SET requests_allowed = false
WHERE id IN (
    SELECT user_id FROM request_user_limits
    WHERE limit_mode = 'blocked' OR approval_mode = 'blocked'
);
UPDATE request_user_limits
SET limit_mode = CASE WHEN limit_mode = 'blocked' THEN 'inherit' ELSE limit_mode END,
    approval_mode = CASE WHEN approval_mode = 'blocked' THEN 'inherit' ELSE approval_mode END,
    updated_at = now()
WHERE limit_mode = 'blocked' OR approval_mode = 'blocked';

-- +goose Down
-- Accounts moved to requests_allowed = false stay blocked that way.
DROP TABLE request_group_limits;
