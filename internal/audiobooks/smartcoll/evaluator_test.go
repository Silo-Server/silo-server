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

// oneRule is a query matching every candidate that satisfies a single rule.
func oneRule(field, op string, value any) QueryDefinition {
	return QueryDefinition{
		Match:  "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: field, Op: op, Value: value}}}},
	}
}

func TestEvaluateRules(t *testing.T) {
	finished := func(c []Candidate) { c[0].IsFinished = true }
	tests := []struct {
		name   string
		qd     QueryDefinition
		mutate func([]Candidate)
		opts   EvaluateOptions
		want   int
		why    string
	}{
		{name: "GenreContains", qd: oneRule("genre", "contains", "Sci"), want: 2, why: "sci-fi books"},
		{name: "YearBetween", qd: oneRule("year", "between", []any{2000, 2025}), want: 2, why: "Mistborn + Project Hail Mary"},
		{name: "AddedInLast14d", qd: oneRule("added_at", "in_last", "14d"), opts: EvaluateOptions{Now: time.Now()}, want: 1, why: "only Project Hail Mary within 14d"},
		{
			name: "MatchAny",
			qd: QueryDefinition{
				Match: "any",
				Groups: []QueryGroup{
					{Match: "all", Rules: []QueryRule{{Field: "author", Op: "is", Value: "Andy Weir"}}},
					{Match: "all", Rules: []QueryRule{{Field: "year", Op: "lt", Value: 1970}}},
				},
			},
			want: 2, why: "Project Hail Mary + Dune",
		},
		{name: "PersonalizedDroppedWithoutScope", qd: oneRule("finished", "is", true), mutate: finished, opts: EvaluateOptions{AllowPersonalized: false}, want: 0, why: "personalization dropped"},
		{name: "PersonalizedWithScope", qd: oneRule("finished", "is", true), mutate: finished, opts: EvaluateOptions{AllowPersonalized: true}, want: 1, why: "Mistborn, personalization on"},
		{
			name: "BookmarkCountGT",
			qd:   oneRule("bookmark_count", "gt", 0),
			mutate: func(c []Candidate) {
				c[0].BookmarkCount = 5
				c[1].BookmarkCount = 0
				c[2].BookmarkCount = 2
			},
			opts: EvaluateOptions{AllowPersonalized: true}, want: 2, why: "Mistborn + Dune",
		},
		{
			name: "AbandonedRule",
			qd:   oneRule("abandoned", "is", true),
			mutate: func(c []Candidate) {
				c[0].CurrentSeconds = 1000
				c[0].LastPlayedAt = time.Now().Add(-90 * 24 * time.Hour)
			},
			opts: EvaluateOptions{AllowPersonalized: true, AbandonedAfter: 60 * 24 * time.Hour, Now: time.Now()},
			want: 1, why: "Mistborn abandoned 90d ago",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cands := sampleCandidates()
			if tt.mutate != nil {
				tt.mutate(cands)
			}
			got := Evaluate(context.Background(), tt.qd, cands, tt.opts)
			if len(got) != tt.want {
				t.Errorf("matched %d, want %d (%s)", len(got), tt.want, tt.why)
			}
		})
	}
}
