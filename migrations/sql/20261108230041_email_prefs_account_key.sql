-- +goose Up
-- +goose StatementBegin
-- A profile id is only unique within its account: every account from before
-- profiles has a profile named 'default'. notification_email_prefs was keyed
-- by profile id alone, so two accounts' 'default' profiles shared one row.
-- Each saw the other's mode and verified address, and whichever account last
-- changed its mode or requested an address took the row over, address
-- included: its email then went to the other account's address. Key the row
-- by account and profile.
--
-- A row whose profile id more than one account uses cannot be split back:
-- nothing records which account verified its address, so it may now belong to
-- the account that took it over. Such rows are reset to unconfigured (mode
-- off, no verified or pending address, no unsubscribe token) rather than
-- guessing an owner. Each account's profile then verifies its own address
-- again. The unsubscribe token goes too: links already sent to the old
-- address would otherwise switch off the reset row's future mail. The
-- accounts that use a profile id come from user_profiles and the
-- account-keyed notification tables, as in 20261016161203.
UPDATE public.notification_email_prefs p SET
    mode = 'off',
    custom_email = '',
    pending_email = '',
    pending_token_hash = '',
    pending_expires_at = NULL,
    unsubscribe_token = '',
    updated_at = now()
WHERE EXISTS (
    SELECT 1 FROM (
        SELECT user_id, id AS profile_id FROM public.user_profiles
        UNION SELECT user_id, profile_id FROM public.profile_series_interest
        UNION SELECT user_id, profile_id FROM public.notification_deliveries
        UNION SELECT user_id, profile_id FROM public.notification_preferences
        UNION SELECT user_id, profile_id FROM public.notification_email_verifications
    ) owner
    WHERE owner.profile_id = p.profile_id AND owner.user_id <> p.user_id
);

-- Every row has one user_id and the table held one row per profile id, so the
-- wider key holds every existing row. It leads with user_id, which serves the
-- per-account reads notification_email_prefs_user_idx did.
ALTER TABLE public.notification_email_prefs
    DROP CONSTRAINT notification_email_prefs_pkey,
    ADD CONSTRAINT notification_email_prefs_pkey PRIMARY KEY (user_id, profile_id);
DROP INDEX public.notification_email_prefs_user_idx;

DROP INDEX public.notification_email_verifications_profile_idx;
CREATE INDEX notification_email_verifications_account_profile_idx
    ON public.notification_email_verifications (user_id, profile_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The profile-only key cannot hold two accounts' rows for one profile id.
-- Refuse instead of choosing which account's email settings to delete. Rows
-- the Up migration reset stay reset.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.notification_email_prefs
               GROUP BY profile_id HAVING count(*) > 1) THEN
        RAISE EXCEPTION 'email notification settings for profiles with the same id on different accounts cannot be keyed by profile id alone';
    END IF;
END;
$$;

DROP INDEX public.notification_email_verifications_account_profile_idx;
CREATE INDEX notification_email_verifications_profile_idx
    ON public.notification_email_verifications (profile_id);

ALTER TABLE public.notification_email_prefs
    DROP CONSTRAINT notification_email_prefs_pkey,
    ADD CONSTRAINT notification_email_prefs_pkey PRIMARY KEY (profile_id);
CREATE INDEX notification_email_prefs_user_idx
    ON public.notification_email_prefs (user_id);
-- +goose StatementEnd
