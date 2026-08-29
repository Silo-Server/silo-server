-- +goose Up
-- Admin accounts are never members of an access group: group ceilings (stream
-- caps, library lists) must not apply to the server operator. Create has always
-- left admins ungrouped, but until now an account promoted to admin kept its
-- group, and an existing admin could be placed in one. Clear those rows and
-- bump the policy revision so cached session policy is re-read.
--
-- Deliberately no CHECK constraint yet: during a rolling upgrade, nodes on the
-- previous version still promote without clearing the group, and a constraint
-- would turn that into a 500. The repository resolves the group against the
-- row's role inside each write and the policy resolver ignores a group on an
-- admin, so residual rows are harmless; add the constraint in a later release
-- once every writer is on this version.
UPDATE users
SET access_group_id = NULL,
    access_policy_revision = access_policy_revision + 1,
    updated_at = NOW()
WHERE role = 'admin' AND access_group_id IS NOT NULL;

-- A pending admin invitation created before this rule advertised a group that
-- accept would now drop; clear it so the invitation shows what it will do.
UPDATE invitations
SET access_group_id = NULL
WHERE role = 'admin'
  AND access_group_id IS NOT NULL
  AND accepted_at IS NULL
  AND revoked_at IS NULL;

-- +goose Down
-- Data-only backfill; the cleared group memberships are not recoverable.
SELECT 1;
