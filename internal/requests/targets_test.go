package requests

import "testing"

func TestAggregateStatus(t *testing.T) {
	cases := []struct {
		name    string
		targets []Target
		status  Status
		outcome Outcome
	}{
		{"all completed", []Target{{Status: StatusCompleted}, {Status: StatusCompleted}}, StatusCompleted, OutcomeActive},
		{"one downloading", []Target{{Status: StatusCompleted}, {Status: StatusDownloading}}, StatusDownloading, OutcomeActive},
		{"queued only", []Target{{Status: StatusQueued}}, StatusQueued, OutcomeActive},
		{"all failed", []Target{{Status: StatusFailed}, {Status: StatusFailed}}, StatusQueued, OutcomeFailed},
		{"completed plus failed", []Target{{Status: StatusCompleted}, {Status: StatusFailed}}, StatusQueued, OutcomeFailed},
		{"partial fail stays active", []Target{{Status: StatusFailed}, {Status: StatusDownloading}}, StatusDownloading, OutcomeActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotOutcome := aggregateStatus(tc.targets)
			if gotStatus != tc.status || gotOutcome != tc.outcome {
				t.Fatalf("aggregateStatus = (%s,%s), want (%s,%s)", gotStatus, gotOutcome, tc.status, tc.outcome)
			}
		})
	}
}
