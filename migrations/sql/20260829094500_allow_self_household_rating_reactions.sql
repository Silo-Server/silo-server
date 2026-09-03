-- +goose Up
ALTER TABLE household_rating_reactions
    DROP CONSTRAINT IF EXISTS household_rating_reactions_no_self_check;

-- +goose Down
DELETE FROM household_rating_reactions
WHERE target_user_id = reactor_user_id
  AND target_profile_id = reactor_profile_id;

ALTER TABLE household_rating_reactions
    ADD CONSTRAINT household_rating_reactions_no_self_check CHECK (
        target_user_id <> reactor_user_id OR target_profile_id <> reactor_profile_id
    );
