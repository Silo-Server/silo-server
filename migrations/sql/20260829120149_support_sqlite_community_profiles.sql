-- +goose Up
ALTER TABLE household_rating_reactions
    DROP CONSTRAINT IF EXISTS household_rating_reactions_reactor_profile_fkey;

ALTER TABLE household_rating_reactions
    ADD CONSTRAINT household_rating_reactions_reactor_user_fkey FOREIGN KEY (
        reactor_user_id
    ) REFERENCES users (id)
      ON DELETE CASCADE;

-- +goose Down
ALTER TABLE household_rating_reactions
    DROP CONSTRAINT IF EXISTS household_rating_reactions_reactor_user_fkey;

DELETE FROM household_rating_reactions reaction
WHERE NOT EXISTS (
    SELECT 1
    FROM user_profiles profile
    WHERE profile.user_id = reaction.reactor_user_id
      AND profile.id = reaction.reactor_profile_id
);

ALTER TABLE household_rating_reactions
    ADD CONSTRAINT household_rating_reactions_reactor_profile_fkey FOREIGN KEY (
        reactor_user_id,
        reactor_profile_id
    ) REFERENCES user_profiles (user_id, id)
      ON DELETE CASCADE;
