-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.acl_groups (
    id bigserial PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    built_in boolean NOT NULL DEFAULT false,
    protected boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
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

INSERT INTO public.acl_groups (slug, name, description, built_in, protected)
VALUES
    ('owner', 'Owner', 'Full server ownership and security control.', true, true),
    ('admin', 'Admin', 'Broad operational administration.', true, true),
    ('library_manager', 'Library Manager', 'Library and scan management.', true, false),
    ('metadata_curator', 'Metadata Curator', 'Metadata, poster, marker, and provider curation.', true, false),
    ('viewer', 'Viewer', 'Normal media playback access.', true, false),
    ('restricted_viewer', 'Restricted Viewer', 'Playback access with tighter limits.', true, false)
ON CONFLICT (slug) DO UPDATE
SET name = EXCLUDED.name,
    description = EXCLUDED.description,
    built_in = EXCLUDED.built_in,
    protected = EXCLUDED.protected,
    updated_at = now();

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

WITH viewer_group AS (
    SELECT id FROM public.acl_groups WHERE slug = 'viewer'
)
INSERT INTO public.acl_group_members (group_id, user_id)
SELECT viewer_group.id, users.id
FROM public.users, viewer_group
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
