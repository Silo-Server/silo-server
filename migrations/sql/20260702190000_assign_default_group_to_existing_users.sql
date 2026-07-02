-- +goose Up
-- +goose StatementBegin
-- Bring existing (pre-groups) users under the seeded Default Group so the
-- whole instance is governed by one policy source. Admin accounts are left
-- ungrouped: scope/action decisions are role-blind, so grouping an admin
-- would cap the server owner's streams and transcoded downloads on upgrade.
--
-- Per-user limits still holding the retired column defaults (6 streams /
-- 2 transcodes) are normalized to 0 (= unrestricted at the user layer) in the
-- same statement, so the group's ceilings actually govern migrated users.
-- Values that differ from the old defaults were admin-chosen and are kept.
UPDATE public.users u
SET access_group_id = g.id,
    max_streams = CASE WHEN u.max_streams = 6 THEN 0 ELSE u.max_streams END,
    max_transcodes = CASE WHEN u.max_transcodes = 2 THEN 0 ELSE u.max_transcodes END
FROM public.access_groups g
WHERE g.is_default
  AND u.access_group_id IS NULL
  AND u.role <> 'admin';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Best-effort inverse: ungroup non-admin members of the default group and
-- restore the old column defaults where the Up normalized them.
UPDATE public.users u
SET access_group_id = NULL,
    max_streams = CASE WHEN u.max_streams = 0 THEN 6 ELSE u.max_streams END,
    max_transcodes = CASE WHEN u.max_transcodes = 0 THEN 2 ELSE u.max_transcodes END
FROM public.access_groups g
WHERE g.is_default
  AND u.access_group_id = g.id
  AND u.role <> 'admin';
-- +goose StatementEnd
