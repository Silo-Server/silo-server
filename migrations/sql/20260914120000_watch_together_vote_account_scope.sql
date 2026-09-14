-- +goose Up
-- +goose StatementBegin
ALTER TABLE watch_together_votes
    ADD COLUMN voter_user_id INTEGER NOT NULL DEFAULT 0;

-- Historical votes predate account identity. Keep their tally, but use the
-- reserved zero value so they cannot be attributed to a real account.

ALTER TABLE watch_together_votes
    DROP CONSTRAINT watch_together_votes_pkey,
    ADD CONSTRAINT watch_together_votes_pkey PRIMARY KEY (suggestion_id, voter_user_id, voter_profile_id);

ALTER TABLE watch_together_votes
    ALTER COLUMN voter_user_id DROP DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM watch_together_votes a
USING watch_together_votes b
WHERE a.ctid < b.ctid
  AND a.suggestion_id = b.suggestion_id
  AND a.voter_profile_id = b.voter_profile_id;

UPDATE watch_together_suggestions s
SET vote_count = COALESCE(v.count, 0)
FROM (
    SELECT s2.id, COUNT(v2.suggestion_id)::integer AS count
    FROM watch_together_suggestions s2
    LEFT JOIN watch_together_votes v2 ON v2.suggestion_id = s2.id
    GROUP BY s2.id
) v
WHERE s.id = v.id;

ALTER TABLE watch_together_votes
    DROP CONSTRAINT watch_together_votes_pkey,
    ADD CONSTRAINT watch_together_votes_pkey PRIMARY KEY (suggestion_id, voter_profile_id),
    DROP COLUMN voter_user_id;
-- +goose StatementEnd
