package metadata

import "testing"

func TestRefreshFollowUpContentID(t *testing.T) {
	// After a refresh, follow-up writes (stale-ID clear/record, debt sync) must
	// target the canonical content ID the item was persisted/merged into, not
	// the requested ID — which may have been deleted when the item was
	// canonicalized into an existing one during mergeAndPersist.
	if got := refreshFollowUpContentID("req", nil); got != "req" {
		t.Fatalf("nil result: got %q, want %q", got, "req")
	}
	if got := refreshFollowUpContentID("req", &ProcessResult{ContentID: ""}); got != "req" {
		t.Fatalf("empty result id: got %q, want %q", got, "req")
	}
	if got := refreshFollowUpContentID("req", &ProcessResult{ContentID: "   "}); got != "req" {
		t.Fatalf("blank result id: got %q, want %q", got, "req")
	}
	if got := refreshFollowUpContentID("req", &ProcessResult{ContentID: "canonical"}); got != "canonical" {
		t.Fatalf("canonicalized result id: got %q, want %q", got, "canonical")
	}
}
