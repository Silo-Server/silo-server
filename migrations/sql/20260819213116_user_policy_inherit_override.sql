-- +goose Up
-- +goose StatementBegin
-- User policy fields move from "strictest of user and group wins" to
-- inherit/override: NULL on the user row means "inherit the access group's
-- value"; a non-NULL value is an explicit per-user override that replaces the
-- group value for that field in either direction (grant or restrict).
--
-- Access groups gain the two transcode gates so every user-level field has a
-- group value to inherit.
ALTER TABLE public.access_groups
    ADD COLUMN transcode_allowed boolean NOT NULL DEFAULT true,
    ADD COLUMN audio_transcode_allowed boolean NOT NULL DEFAULT true;

-- Users may now override the group's media-request gate as well.
ALTER TABLE public.users
    ADD COLUMN requests_allowed boolean;

-- Drop NOT NULL and the column defaults: a fresh user row inherits everything.
ALTER TABLE public.users
    ALTER COLUMN max_playback_quality DROP NOT NULL,
    ALTER COLUMN max_playback_quality DROP DEFAULT,
    ALTER COLUMN max_streams DROP DEFAULT,
    ALTER COLUMN max_transcodes DROP DEFAULT,
    ALTER COLUMN transcode_allowed DROP NOT NULL,
    ALTER COLUMN transcode_allowed DROP DEFAULT,
    ALTER COLUMN audio_transcode_allowed DROP NOT NULL,
    ALTER COLUMN audio_transcode_allowed DROP DEFAULT,
    ALTER COLUMN download_allowed DROP DEFAULT,
    ALTER COLUMN download_transcode_allowed DROP DEFAULT;

-- Behavior-preserving mapping of existing rows. Under the old merge a user
-- value of 0 / '' / true meant "no opinion at the user layer, the group
-- decides", so those become NULL (inherit). Restrictive values (false,
-- positive caps, a named quality, an explicit library list) stay as explicit
-- overrides. The one deliberate change: a positive cap that exceeds the
-- group's cap now wins instead of being clamped.
--
-- Caps map every value <= 0, not just 0: the admin API used to accept a
-- negative cap and the old resolver treated anything <= 0 as "defer to the
-- group". Keeping a negative would turn it into an override that resolves to
-- 0, i.e. unlimited — the opposite of what the row meant.
--
-- Boolean mapping: NOT col (rather than a bare ELSE) keeps a pre-existing
-- NULL — possible on the download columns, which were always nullable — as
-- NULL/inherit instead of inventing an explicit deny override.
--
-- download_transcode_allowed is the exception and maps the other way round.
-- It is the one policy column whose old column default was false, so a
-- never-touched account already stores false. Mapping false to an explicit
-- deny would freeze every existing account against its group and make the
-- group's toggle inert; instead false becomes NULL (inherit) and only an
-- explicit true is kept as an override. The permissive default for an
-- ungrouped account (access.NoGroupPolicy) is false for this field to match
-- the old column default, as is the seeded Default Group.
UPDATE public.users SET
    max_streams = CASE WHEN max_streams > 0 THEN max_streams END,
    max_transcodes = CASE WHEN max_transcodes > 0 THEN max_transcodes END,
    max_playback_quality = NULLIF(max_playback_quality, ''),
    transcode_allowed = CASE WHEN NOT transcode_allowed THEN false ELSE NULL END,
    audio_transcode_allowed = CASE WHEN NOT audio_transcode_allowed THEN false ELSE NULL END,
    download_allowed = CASE WHEN NOT download_allowed THEN false ELSE NULL END,
    download_transcode_allowed = CASE WHEN download_transcode_allowed THEN true ELSE NULL END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Lossy by construction: the old schema cannot represent the distinction
-- between inherit and an explicit permissive override. An explicit unlimited
-- cap (0), an explicit '' quality, and explicit-true booleans collapse into
-- the old delegate sentinels — under restored strictest-merge semantics such
-- users fall back to their group's values. requests_allowed overrides are
-- dropped entirely. download_transcode_allowed collapses to false, the
-- restored column default and the value the Up direction treats as inherit.
UPDATE public.users SET
    max_streams = COALESCE(max_streams, 0),
    max_transcodes = COALESCE(max_transcodes, 0),
    max_playback_quality = COALESCE(max_playback_quality, ''),
    transcode_allowed = COALESCE(transcode_allowed, true),
    audio_transcode_allowed = COALESCE(audio_transcode_allowed, true),
    download_allowed = COALESCE(download_allowed, true),
    download_transcode_allowed = COALESCE(download_transcode_allowed, false);

ALTER TABLE public.users
    ALTER COLUMN max_playback_quality SET DEFAULT '',
    ALTER COLUMN max_playback_quality SET NOT NULL,
    ALTER COLUMN max_streams SET DEFAULT 0,
    ALTER COLUMN max_transcodes SET DEFAULT 0,
    ALTER COLUMN transcode_allowed SET DEFAULT true,
    ALTER COLUMN transcode_allowed SET NOT NULL,
    ALTER COLUMN audio_transcode_allowed SET DEFAULT true,
    ALTER COLUMN audio_transcode_allowed SET NOT NULL,
    ALTER COLUMN download_allowed SET DEFAULT true,
    ALTER COLUMN download_transcode_allowed SET DEFAULT false;

ALTER TABLE public.users
    DROP COLUMN IF EXISTS requests_allowed;

ALTER TABLE public.access_groups
    DROP COLUMN IF EXISTS transcode_allowed,
    DROP COLUMN IF EXISTS audio_transcode_allowed;
-- +goose StatementEnd
