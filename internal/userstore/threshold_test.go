package userstore

import "testing"

// Reaching the end credits finishes an item below the watched threshold, and
// that is decided before the min-resume floor: credits that start under a
// raised floor still finish the play instead of discarding it.
func TestResolveProgressStateEndCredits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		thresholds    ProgressThresholds
		position      float64
		wantPosition  float64
		wantCompleted bool
		wantSkip      bool
	}{
		{name: "inside credits", thresholds: ProgressThresholds{CreditsStartSeconds: 850}, position: 860, wantCompleted: true},
		{name: "before credits", thresholds: ProgressThresholds{CreditsStartSeconds: 850}, position: 840, wantPosition: 840},
		{name: "credits under a raised floor", thresholds: ProgressThresholds{WatchedPct: 90, MinResumePct: 88, CreditsStartSeconds: 850}, position: 860, wantCompleted: true},
		{name: "below the floor before credits", thresholds: ProgressThresholds{WatchedPct: 90, MinResumePct: 88, CreditsStartSeconds: 850}, position: 840, wantPosition: 840, wantSkip: true},
		{name: "credits at the end are ignored", thresholds: ProgressThresholds{CreditsStartSeconds: 1000}, position: 880, wantPosition: 880},
	} {
		t.Run(tc.name, func(t *testing.T) {
			position, completed, skip := ResolveProgressState(tc.position, 1000, tc.thresholds)
			if position != tc.wantPosition || completed != tc.wantCompleted || skip != tc.wantSkip {
				t.Fatalf("= (%v, %v, %v), want (%v, %v, %v)", position, completed, skip, tc.wantPosition, tc.wantCompleted, tc.wantSkip)
			}
		})
	}
}
