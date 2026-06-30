-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.acl_groups
    ADD COLUMN IF NOT EXISTS policy jsonb;

UPDATE public.acl_groups
SET policy = '{}'::jsonb,
    updated_at = now()
WHERE policy IS NULL
   OR jsonb_typeof(policy) <> 'object';

ALTER TABLE public.acl_groups
    ALTER COLUMN policy SET DEFAULT '{}'::jsonb;

ALTER TABLE public.acl_groups
    ALTER COLUMN policy SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.acl_groups'::regclass
          AND conname = 'acl_groups_policy_object_check'
    ) THEN
        ALTER TABLE public.acl_groups
            ADD CONSTRAINT acl_groups_policy_object_check CHECK (jsonb_typeof(policy) = 'object');
    END IF;
END $$;

INSERT INTO public.acl_policy_revisions (id, revision)
VALUES (true, 1)
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.acl_groups (slug, name, description, policy, built_in, protected)
VALUES
    ('owner', 'Owner', 'Full server ownership and security control.', '{}'::jsonb, true, true),
    ('admin', 'Admin', 'Broad operational administration.', '{}'::jsonb, true, true),
    ('library_manager', 'Library Manager', 'Library and scan management.', '{}'::jsonb, true, false),
    ('metadata_curator', 'Metadata Curator', 'Metadata, poster, marker, and provider curation.', '{}'::jsonb, true, false),
    ('standard_user', 'User', 'Normal media access.', '{}'::jsonb, true, false),
    ('restricted_user', 'Restricted User', 'Media access with tighter limits.', '{}'::jsonb, true, false)
ON CONFLICT (slug) DO UPDATE
SET name = EXCLUDED.name,
    description = EXCLUDED.description,
    policy = CASE
        WHEN jsonb_typeof(public.acl_groups.policy) = 'object' THEN public.acl_groups.policy
        ELSE '{}'::jsonb
    END,
    built_in = EXCLUDED.built_in,
    protected = EXCLUDED.protected,
    updated_at = now();

WITH built_in_user_grants(subject_id, action, resource_type, name) AS (
    VALUES
        ('owner', 'playback.play', 'media_item', 'Owner playback'),
        ('owner', 'playback.transcode', 'media_item', 'Owner transcoding'),
        ('owner', 'profiles.manage', 'profile', 'Owner profile management'),
        ('owner', 'personal_lists.manage', 'media_item', 'Owner personal lists'),
        ('owner', 'requests.create', 'request', 'Owner request creation'),
        ('admin', 'playback.play', 'media_item', 'Admin playback'),
        ('admin', 'playback.transcode', 'media_item', 'Admin transcoding'),
        ('admin', 'profiles.manage', 'profile', 'Admin profile management'),
        ('admin', 'personal_lists.manage', 'media_item', 'Admin personal lists'),
        ('admin', 'requests.create', 'request', 'Admin request creation'),
        ('standard_user', 'playback.play', 'media_item', 'User playback'),
        ('standard_user', 'playback.transcode', 'media_item', 'User transcoding'),
        ('standard_user', 'profiles.manage', 'profile', 'User profile management'),
        ('standard_user', 'personal_lists.manage', 'media_item', 'User personal lists'),
        ('standard_user', 'requests.create', 'request', 'User request creation'),
        ('restricted_user', 'playback.play', 'media_item', 'Restricted user playback'),
        ('restricted_user', 'playback.transcode', 'media_item', 'Restricted user transcoding'),
        ('restricted_user', 'profiles.manage', 'profile', 'Restricted user profile management'),
        ('restricted_user', 'personal_lists.manage', 'media_item', 'Restricted user personal lists'),
        ('restricted_user', 'requests.create', 'request', 'Restricted user request creation')
),
deleted_hidden_default_grants AS (
    DELETE FROM public.acl_rules AS hidden
    USING built_in_user_grants AS grant_row
    WHERE hidden.subject_type = 'builtin_role'
      AND hidden.subject_id = grant_row.subject_id
      AND hidden.action = grant_row.action
      AND hidden.resource_type = grant_row.resource_type
      AND hidden.resource_id = '*'
    RETURNING hidden.id
)
INSERT INTO public.acl_rules (
    subject_type, subject_id, action, resource_type, resource_id, effect, conditions, priority, name
)
SELECT
    'group',
    grant_row.subject_id,
    grant_row.action,
    grant_row.resource_type,
    '*',
    'allow',
    '{}'::jsonb,
    10,
    grant_row.name
FROM built_in_user_grants AS grant_row
WHERE NOT EXISTS (
    SELECT 1
    FROM public.acl_rules existing
    WHERE existing.subject_type = 'group'
      AND existing.subject_id = grant_row.subject_id
      AND existing.action = grant_row.action
      AND existing.resource_type = grant_row.resource_type
      AND existing.resource_id = '*'
);

WITH oldest_admin AS (
    SELECT id
    FROM public.users
    WHERE enabled = true AND role = 'admin'
    ORDER BY created_at ASC, id ASC
    LIMIT 1
),
owner_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'owner'
)
INSERT INTO public.acl_group_members (group_id, user_id)
SELECT owner_group.id, oldest_admin.id
FROM owner_group, oldest_admin
ON CONFLICT DO NOTHING;

WITH oldest_admin AS (
    SELECT id
    FROM public.users
    WHERE enabled = true AND role = 'admin'
    ORDER BY created_at ASC, id ASC
    LIMIT 1
),
admin_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'admin'
)
INSERT INTO public.acl_group_members (group_id, user_id)
SELECT admin_group.id, users.id
FROM public.users, admin_group
WHERE users.role = 'admin'
  AND NOT EXISTS (
      SELECT 1
      FROM oldest_admin
      WHERE oldest_admin.id = users.id
  )
ON CONFLICT DO NOTHING;

WITH standard_user_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'standard_user'
)
INSERT INTO public.acl_group_members (group_id, user_id)
SELECT standard_user_group.id, users.id
FROM public.users, standard_user_group
WHERE COALESCE(users.role, 'user') <> 'admin'
ON CONFLICT DO NOTHING;

UPDATE public.acl_policy_revisions
SET revision = revision + 1,
    updated_at = now()
WHERE id = true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
