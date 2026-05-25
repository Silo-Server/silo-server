package arrclient

import "testing"

func TestEvaluateQueueFailureWinsOverDownloading(t *testing.T) {
	result := EvaluateQueue([]QueueResource{
		{Status: "downloading", TrackedDownloadState: "downloading"},
		{Title: "Bad Release", Status: "queued", TrackedDownloadState: "failedPending"},
	})

	if result.State != QueueStateFailed {
		t.Fatalf("state = %q, want failed", result.State)
	}
	if result.ExternalStatus != "queued/failedPending" {
		t.Fatalf("external status = %q, want queued/failedPending", result.ExternalStatus)
	}
	if result.Message == "" {
		t.Fatal("message should describe the queue failure")
	}
}
