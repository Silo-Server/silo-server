-- +goose Up
-- +goose StatementBegin
CREATE TABLE watch_together_suggestions (
    id                    TEXT        PRIMARY KEY,
    room_id               TEXT        NOT NULL REFERENCES watch_together_rooms(id) ON DELETE CASCADE,
    suggester_user_id     INTEGER     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    suggester_profile_id  TEXT        NOT NULL,
    content_id            TEXT        NOT NULL,
    content_type          TEXT        NOT NULL,
    title                 TEXT        NOT NULL,
    subtitle              TEXT        NOT NULL DEFAULT '',
    poster_url            TEXT        NOT NULL DEFAULT '',
    note                  TEXT        NOT NULL DEFAULT '',
    vote_count            INTEGER     NOT NULL DEFAULT 0,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT wt_suggestion_content_type_check CHECK (content_type IN ('movie', 'episode'))
);

CREATE TABLE watch_together_votes (
    suggestion_id    TEXT NOT NULL REFERENCES watch_together_suggestions(id) ON DELETE CASCADE,
    voter_profile_id TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (suggestion_id, voter_profile_id)
);

CREATE INDEX idx_wt_suggestions_room ON watch_together_suggestions(room_id, vote_count DESC, created_at);
CREATE INDEX idx_wt_votes_suggestion ON watch_together_votes(suggestion_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_wt_votes_suggestion;
DROP INDEX IF EXISTS idx_wt_suggestions_room;
DROP TABLE IF EXISTS watch_together_votes;
DROP TABLE IF EXISTS watch_together_suggestions;
-- +goose StatementEnd
