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
    value
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
    END
FROM public.user_setting_values AS legacy
WHERE legacy.key = 'playback.auto_skip_intro'
ON CONFLICT DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM public.user_setting_values
WHERE key = 'playback.intro_skip_mode';
-- +goose StatementEnd
