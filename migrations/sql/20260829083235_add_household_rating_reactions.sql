-- +goose Up
CREATE INDEX IF NOT EXISTS idx_user_ratings_media_item_rated
    ON user_ratings (media_item_id, rated_at DESC);

CREATE TABLE household_rating_reactions (
    media_item_id text NOT NULL,
    target_user_id integer NOT NULL,
    target_profile_id text NOT NULL,
    reactor_user_id integer NOT NULL,
    reactor_profile_id text NOT NULL,
    reaction smallint NOT NULL,
    reacted_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT household_rating_reactions_pkey PRIMARY KEY (
        media_item_id,
        target_user_id,
        target_profile_id,
        reactor_user_id,
        reactor_profile_id
    ),
    CONSTRAINT household_rating_reactions_value_check CHECK (reaction IN (-1, 1)),
    CONSTRAINT household_rating_reactions_no_self_check CHECK (
        target_user_id <> reactor_user_id OR target_profile_id <> reactor_profile_id
    ),
    CONSTRAINT household_rating_reactions_target_fkey FOREIGN KEY (
        target_user_id,
        target_profile_id,
        media_item_id
    ) REFERENCES user_ratings (user_id, profile_id, media_item_id)
      ON UPDATE CASCADE
      ON DELETE CASCADE
);

-- +goose Down
DROP TABLE IF EXISTS household_rating_reactions;
DROP INDEX IF EXISTS idx_user_ratings_media_item_rated;
