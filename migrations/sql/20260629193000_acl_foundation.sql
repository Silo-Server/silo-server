-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.acl_groups (
    id bigserial PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    built_in boolean NOT NULL DEFAULT false,
    protected boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT acl_groups_policy_object_check CHECK (jsonb_typeof(policy) = 'object')
);

CREATE TABLE IF NOT EXISTS public.acl_group_members (
    group_id bigint NOT NULL REFERENCES public.acl_groups(id) ON DELETE CASCADE,
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS public.acl_rules (
    id bigserial PRIMARY KEY,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL DEFAULT '*',
    effect text NOT NULL,
    conditions jsonb NOT NULL DEFAULT '{}'::jsonb,
    priority integer NOT NULL DEFAULT 0,
    name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT acl_rules_effect_check CHECK (effect IN ('allow', 'deny')),
    CONSTRAINT acl_rules_subject_type_check CHECK (subject_type IN ('user', 'group', 'builtin_role', 'everyone')),
    CONSTRAINT acl_rules_conditions_object_check CHECK (jsonb_typeof(conditions) = 'object')
);

CREATE INDEX IF NOT EXISTS acl_group_members_user_idx ON public.acl_group_members(user_id);
CREATE INDEX IF NOT EXISTS acl_rules_subject_idx ON public.acl_rules(subject_type, subject_id);
CREATE INDEX IF NOT EXISTS acl_rules_action_resource_idx ON public.acl_rules(action, resource_type, resource_id);

CREATE TABLE IF NOT EXISTS public.acl_policy_revisions (
    id boolean PRIMARY KEY DEFAULT true,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT acl_policy_revisions_singleton CHECK (id)
);

INSERT INTO public.acl_policy_revisions (id, revision)
VALUES (true, 1)
ON CONFLICT (id) DO NOTHING;

ALTER TABLE public.users
    ALTER COLUMN permissions SET DEFAULT '{}'::text[];

UPDATE public.users
SET permissions = array_remove(permissions, 'marker_edit'),
    access_policy_revision = access_policy_revision + 1,
    updated_at = now()
WHERE COALESCE(role, 'user') <> 'admin'
  AND 'marker_edit' = ANY (permissions);

INSERT INTO public.acl_groups (slug, name, description, built_in, protected)
VALUES
    ('owner', 'Owner', 'Full server ownership and security control.', true, true),
    ('admin', 'Admin', 'Broad operational administration.', true, true),
    ('library_manager', 'Library Manager', 'Library and scan management.', true, false),
    ('metadata_curator', 'Metadata Curator', 'Metadata, poster, marker, and provider curation.', true, false),
    ('standard_user', 'User', 'Normal media access.', true, false),
    ('restricted_user', 'Restricted User', 'Media access with tighter limits.', true, false)
ON CONFLICT (slug) DO UPDATE
SET name = EXCLUDED.name,
    description = EXCLUDED.description,
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
grant_subjects(subject_type) AS (
    VALUES
        ('group'),
        ('builtin_role')
)
INSERT INTO public.acl_rules (
    subject_type, subject_id, action, resource_type, resource_id, effect, conditions, priority, name
)
SELECT
    grant_subjects.subject_type,
    grant_row.subject_id,
    grant_row.action,
    grant_row.resource_type,
    '*',
    'allow',
    '{}'::jsonb,
    10,
    grant_row.name
FROM built_in_user_grants AS grant_row
CROSS JOIN grant_subjects
WHERE NOT EXISTS (
    SELECT 1
    FROM public.acl_rules existing
    WHERE existing.subject_type = grant_subjects.subject_type
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
WHERE users.enabled = true
  AND users.role = 'admin'
  AND users.id NOT IN (SELECT id FROM oldest_admin)
ON CONFLICT DO NOTHING;

WITH standard_user_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'standard_user'
)
INSERT INTO public.acl_group_members (group_id, user_id)
SELECT standard_user_group.id, users.id
FROM public.users, standard_user_group
WHERE users.enabled = true
  AND COALESCE(users.role, 'user') <> 'admin'
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.acl_policy_revisions;
DROP TABLE IF EXISTS public.acl_rules;
DROP TABLE IF EXISTS public.acl_group_members;
DROP TABLE IF EXISTS public.acl_groups;
-- +goose StatementEnd
