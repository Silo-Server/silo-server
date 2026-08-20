-- Carry every stored playback.auto_skip_intro onto playback.intro_skip_mode.
--
-- Contract revision 7 replaces the boolean with a three-way enum: the boolean
-- could only say "prompt" or "count down then skip", and had no way to turn the
-- prompt off. Both spellings stay live for one release — every shipped client
-- reads the boolean — so each existing choice needs its enum row or a current
-- client would resolve the contract default and quietly discard a preference
-- the household already made.
--
-- Nobody can hold "never" yet, so nothing becomes unrepresentable here.
-- ON CONFLICT DO NOTHING covers the partial unique index on each scope, which
-- makes a re-run a no-op and, more importantly, never overwrites an enum a
-- client wrote itself. See docs/design/2026-08-16-intro-skip-mode.md.
--
-- The timestamps come from the source row rather than defaulting to now(): the
-- admin device surfaces sort and label devices by the canonical row's
-- updated_at, so stamping every backfilled row with the deployment time would
-- make the whole fleet look freshly reconfigured the moment this ran.

-- +goose Up
-- +goose StatementBegin
INSERT INTO public.user_setting_values (
    user_id,
    key,
    scope,
    profile_id,
    client_family,
    device_id,
    library_id,
    series_id,
    value,
    created_at,
    updated_at
)
SELECT
    legacy.user_id,
    'playback.intro_skip_mode',
    legacy.scope,
    legacy.profile_id,
    legacy.client_family,
    legacy.device_id,
    legacy.library_id,
    legacy.series_id,
    CASE WHEN legacy.value = 'true'::jsonb
         THEN '"always"'::jsonb
         ELSE '"ask"'::jsonb
    END,
    legacy.created_at,
    legacy.updated_at
FROM public.user_setting_values AS legacy
WHERE legacy.key = 'playback.auto_skip_intro'
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Deliberately empty: the canonical rows stay.
--
-- This migration records nothing about which rows it created, and by the time
-- anyone rolls back it cannot tell a backfilled row from one a client wrote
-- afterwards. Deleting every playback.intro_skip_mode row would therefore erase
-- explicit "never" and "ask" choices that were never this migration's to make.
-- The rows are harmless to an older server: it does not define the key, so
-- resolution never reaches them, and rolling forward again finds them intact.
SELECT 1;
-- +goose StatementEnd
