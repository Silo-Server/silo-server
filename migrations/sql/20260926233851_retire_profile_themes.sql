-- The web client has one theme, Cinema Dark. Profiles can no longer pick a
-- theme or layer their own token overrides and CSS on it; only the admin
-- customizes it, through the accent color and the ui.admin_theme_vars and
-- ui.admin_custom_css server settings, which this migration leaves alone.
--
-- This deletes the stored profile choices at every scope. The three keys stay
-- in the settings manifest, deprecated, so a stale cached web bundle's write
-- is accepted rather than failing; nothing reads what it writes. The legacy
-- account-level user_settings rows (ui_theme, ui_custom_theme_vars,
-- ui_custom_css) stay, like every other row the settings cutover converted.
--
-- The retired admin default theme and light-theme logo references
-- (branding.default_theme, branding.wordmark_light_ref, branding.mark_light_ref)
-- are deliberately kept: the web client and /api/v2 ignore them, but the
-- frozen /api/v1 branding response still reports them until v1 retires.
--
-- No revision or idempotency bookkeeping needs a matching write: each
-- user_setting_values row carries its own revision, reads resolve an absent
-- row to the contract default, and user_setting_mutations holds only replay
-- receipts. The SQLite user store deletes the same rows at schema v29.

-- +goose Up
DELETE FROM public.user_setting_values
 WHERE key IN ('ui.theme', 'ui.custom_theme_vars', 'ui.custom_css');

-- +goose Down
-- Irreversible: the deleted choices are not recorded anywhere, and an older
-- server resolves each missing profile setting to its default theme with no
-- custom styling.
SELECT 1;
