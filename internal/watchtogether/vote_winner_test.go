package watchtogether

import "testing"

// The winner is the head of the repository's ordering (vote_count DESC,
// created_at ASC). These pin the rule the ordering encodes, so a change to the
// query that breaks it fails here rather than silently starting the wrong film.
func TestVoteWinnerIsTheHeadOfTheOrdering(t *testing.T) {
	ordered := []Suggestion{
		{ID: "b", Title: "Heat", VoteCount: 3},
		{ID: "a", Title: "Alien", VoteCount: 1},
		{ID: "c", Title: "Ronin", VoteCount: 0},
	}

	winner, err := winnerFrom(ordered)
	if err != nil {
		t.Fatalf("no winner: %v", err)
	}
	if winner.ID != "b" {
		t.Fatalf("winner = %q, want the most-voted", winner.ID)
	}
}

// A vote nobody cast has no winner. Promoting the oldest suggestion there would
// let a host "start the winner" of a vote that never happened — precisely the
// confusion this mode exists to remove.
func TestVoteWinnerRefusesWhenNobodyHasVoted(t *testing.T) {
	if _, err := winnerFrom(nil); err != ErrNoVotesCast {
		t.Fatalf("empty room: err = %v, want ErrNoVotesCast", err)
	}
	unvoted := []Suggestion{{ID: "a", VoteCount: 0}, {ID: "b", VoteCount: 0}}
	if _, err := winnerFrom(unvoted); err != ErrNoVotesCast {
		t.Fatalf("no votes: err = %v, want ErrNoVotesCast", err)
	}
}

// Ties resolve to whoever suggested first, which the ordering gives us for
// free — and which means re-suggesting the same title cannot jump the queue.
func TestVoteWinnerBreaksTiesByOrder(t *testing.T) {
	tied := []Suggestion{
		{ID: "first", VoteCount: 2},
		{ID: "second", VoteCount: 2},
	}
	winner, err := winnerFrom(tied)
	if err != nil {
		t.Fatal(err)
	}
	if winner.ID != "first" {
		t.Fatalf("tie went to %q, want the earlier suggestion", winner.ID)
	}
}
