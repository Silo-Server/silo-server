package watchtogether

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSuggestionPageVoteChangesPreserveTraversal(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(t.Context(), `CREATE TEMP TABLE watch_together_suggestions (
 id text PRIMARY KEY, room_id text NOT NULL, suggester_user_id integer NOT NULL, suggester_profile_id text NOT NULL,
 content_id text NOT NULL,content_type text NOT NULL,title text NOT NULL,subtitle text NOT NULL,poster_url text NOT NULL,note text NOT NULL,vote_count integer NOT NULL,created_at timestamptz NOT NULL);
 CREATE TEMP TABLE watch_together_votes (suggestion_id text NOT NULL,voter_user_id integer NOT NULL,voter_profile_id text NOT NULL,created_at timestamptz DEFAULT now(),PRIMARY KEY(suggestion_id,voter_user_id,voter_profile_id));`)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewSuggestionRepository(pool)
	now := time.Date(2026, 9, 6, 0, 0, 0, 123000, time.UTC)
	for _, id := range []string{"a", "b", "c", "foreign"} {
		room := "room"
		if id == "foreign" {
			room = "other"
		}
		_, err = repo.CreateSuggestion(t.Context(), Suggestion{ID: id, RoomID: room, SuggesterUserID: 1, SuggesterProfileID: "host", ContentID: "movie", ContentType: "movie", Title: id, CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, more, err := repo.ListSuggestionsPage(t.Context(), "room", 7, "viewer", 1, nil)
	if err != nil || !more || len(first) != 1 || first[0].ID != "a" {
		t.Fatalf("first=%+v more=%v err=%v", first, more, err)
	}
	if err = repo.AddVote(t.Context(), "c", 7, "viewer"); err != nil {
		t.Fatal(err)
	}
	if err = repo.AddVote(t.Context(), "c", 7, "viewer"); !errors.Is(err, ErrDuplicateVote) {
		t.Fatalf("duplicate=%v", err)
	}
	after := &SuggestionPosition{CreatedAt: first[0].CreatedAt, ID: first[0].ID}
	rest, more, err := repo.ListSuggestionsPage(t.Context(), "room", 7, "viewer", 2, after)
	if err != nil || more || len(rest) != 2 || rest[0].ID != "b" || rest[1].ID != "c" || rest[1].VoteCount != 1 || !rest[1].VotedByMe {
		t.Fatalf("rest=%+v more=%v err=%v", rest, more, err)
	}
	if err = repo.RemoveVote(t.Context(), "c", 7, "viewer"); err != nil {
		t.Fatal(err)
	}
	if err = repo.RemoveVote(t.Context(), "c", 7, "viewer"); !errors.Is(err, ErrNotVoted) {
		t.Fatalf("absent=%v", err)
	}
	last, _, err := repo.ListSuggestionsPage(t.Context(), "room", 7, "viewer", 2, after)
	if err != nil || last[1].VoteCount != 0 || last[1].VotedByMe {
		t.Fatalf("last=%+v err=%v", last, err)
	}
}
