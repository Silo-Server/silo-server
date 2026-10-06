package chapterthumbs

import (
	"context"
	"testing"
	"time"
)

func TestContendedPriorityRetryKeepsNewerTargetAndServesOtherFiles(t *testing.T) {
	service := widthQueueService(&testFileRepo{})
	service.inProgress[42] = struct{}{}
	service.QueuePriorityFileAtPosition(t.Context(), 42, 950)
	service.retryPriorityRequest(ChapterThumbnailRequest{FileID: 42, TargetSeconds: new(850.0)})
	service.finishProcessing(42)
	service.QueuePriorityFileAtPosition(t.Context(), 43, 120)
	service.mu.Lock()
	req, ok := service.popQueuedLocked(true)
	retained := service.queuedPriority[42]
	service.mu.Unlock()
	if !ok || req.FileID != 43 || !req.priority {
		t.Fatalf("other file did not run during contention delay: %+v, %v", req, ok)
	}
	if retained.TargetSeconds == nil || *retained.TargetSeconds != 950 {
		t.Fatalf("retry replaced the newer target: %+v", retained)
	}
	// A waiting priority worker must stop promptly even if the lock holder
	// never releases its session. Use a future deadline to observe that wait.
	service.finishProcessing(43)
	service.mu.Lock()
	service.priorityRetry[42] = time.Now().Add(time.Hour)
	service.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan bool, 1)
	go func() { _, ok := service.nextRequest(ctx, true); done <- ok }()
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("dequeued a delayed retry after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting retry ignored cancellation")
	}
}
