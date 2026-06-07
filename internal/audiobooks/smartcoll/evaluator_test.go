package smartcoll

import (
	"context"
	"testing"
	"time"
)

func sampleCandidates() []Candidate {
	return []Candidate{
		{Item: Item{ID: "1", Title: "Mistborn", Authors: []string{"Brandon Sanderson"}, Genres: []string{"Fantasy"}, Year: 2006, Rating: 4.5, AddedAt: time.Now().Add(-30 * 24 * time.Hour), DurationSeconds: 60000}},
		{Item: Item{ID: "2", Title: "Project Hail Mary", Authors: []string{"Andy Weir"}, Genres: []string{"Sci-Fi"}, Year: 2021, Rating: 4.8, AddedAt: time.Now().Add(-10 * 24 * time.Hour), DurationSeconds: 50000}},
		{Item: Item{ID: "3", Title: "Dune", Authors: []string{"Frank Herbert"}, Genres: []string{"Sci-Fi"}, Year: 1965, Rating: 4.3, AddedAt: time.Now().Add(-365 * 24 * time.Hour), DurationSeconds: 75000}},
	}
}

func TestEvaluate_EmptyRulesMatchesAll(t *testing.T) {
	got := Evaluate(context.Background(), QueryDefinition{}, sampleCandidates(), EvaluateOptions{})
	if len(got) != 3 {
		t.Errorf("matched %d, want 3", len(got))
	}
}

func TestEvaluate_GenreContains(t *testing.T) {
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "genre", Op: "contains", Value: "Sci"}}}},
	}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 2 {
		t.Errorf("matched %d, want 2 (sci-fi books)", len(got))
	}
}

func TestEvaluate_YearBetween(t *testing.T) {
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "year", Op: "between", Value: []any{2000, 2025}}}}},
	}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 2 {
		t.Errorf("matched %d, want 2 (Mistborn + Project Hail Mary)", len(got))
	}
}

func TestEvaluate_AddedInLast14d(t *testing.T) {
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "added_at", Op: "in_last", Value: "14d"}}}},
	}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{Now: time.Now()})
	if len(got) != 1 {
		t.Errorf("matched %d, want 1 (only Project Hail Mary within 14d)", len(got))
	}
}

func TestEvaluate_MatchAny(t *testing.T) {
	qd := QueryDefinition{
		Match: "any",
		Groups: []QueryGroup{
			{Match: "all", Rules: []QueryRule{{Field: "author", Op: "is", Value: "Andy Weir"}}},
			{Match: "all", Rules: []QueryRule{{Field: "year", Op: "lt", Value: 1970}}},
		},
	}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 2 {
		t.Errorf("matched %d, want 2 (Project Hail Mary + Dune)", len(got))
	}
}

func TestEvaluate_PersonalizedDroppedWithoutScope(t *testing.T) {
	cands := sampleCandidates()
	cands[0].IsFinished = true
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "finished", Op: "is", Value: true}}}},
	}
	got := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: false})
	if len(got) != 0 {
		t.Errorf("matched %d with personalization dropped, want 0", len(got))
	}
	got2 := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: true})
	if len(got2) != 1 {
		t.Errorf("matched %d with personalization on, want 1 (Mistborn)", len(got2))
	}
}

func TestEvaluate_BookmarkCountGT(t *testing.T) {
	cands := sampleCandidates()
	cands[0].BookmarkCount = 5
	cands[1].BookmarkCount = 0
	cands[2].BookmarkCount = 2
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "bookmark_count", Op: "gt", Value: 0}}}},
	}
	got := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: true})
	if len(got) != 2 {
		t.Errorf("matched %d, want 2", len(got))
	}
}

func TestEvaluate_SortTitleAsc(t *testing.T) {
	qd := QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 3 || got[0].Item.Title != "Dune" || got[2].Item.Title != "Project Hail Mary" {
		t.Errorf("sort wrong: %v %v %v", got[0].Item.Title, got[1].Item.Title, got[2].Item.Title)
	}
}

func TestEvaluate_RandomDeterministicPerSeed(t *testing.T) {
	qd := QueryDefinition{Sort: QuerySort{Field: "random"}}
	a := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{UserSeed: "u1:c1"})
	b := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{UserSeed: "u1:c1"})
	for i := range a {
		if a[i].Item.ID != b[i].Item.ID {
			t.Errorf("random sort not deterministic at index %d: %v vs %v", i, a[i].Item.ID, b[i].Item.ID)
		}
	}
}

func TestEvaluate_LimitTrims(t *testing.T) {
	limit := 2
	qd := QueryDefinition{Limit: &limit, Sort: QuerySort{Field: "title", Order: "asc"}}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 2 {
		t.Errorf("limit didn't apply: len = %d, want 2", len(got))
	}
}

func TestEvaluate_AbandonedRule(t *testing.T) {
	cands := sampleCandidates()
	cands[0].CurrentSeconds = 1000
	cands[0].LastPlayedAt = time.Now().Add(-90 * 24 * time.Hour)
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "abandoned", Op: "is", Value: true}}}},
	}
	got := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: true, AbandonedAfter: 60 * 24 * time.Hour, Now: time.Now()})
	if len(got) != 1 {
		t.Errorf("matched %d, want 1 (Mistborn abandoned 90d ago)", len(got))
	}
}
