package subsync

import "testing"

func TestUpdateQueueKeepsTheWorkerFree(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	sent := make(chan string, 4)
	q := newUpdateQueue(func(job Job) {
		if job.Phase == "first" {
			close(entered)
			<-release // a player connection that is slow to accept the write
		}
		sent <- job.Phase
	})
	q.offer(Job{Phase: "first"})
	<-entered
	// The worker goes on while the first step is being sent; a newer step
	// replaces one not yet sent.
	q.offer(Job{Phase: "second"})
	q.offer(Job{Phase: "third"})
	close(release)
	if first, next := <-sent, <-sent; first != "first" || next != "third" {
		t.Fatalf("sent %q then %q", first, next)
	}
	q.close()
	q.close() // closing twice is safe
	select {
	case extra := <-sent:
		t.Fatalf("sent %q after close", extra)
	default:
	}
}

func TestUpdateQueueCloseWaitsForTheStepBeingSent(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	q := newUpdateQueue(func(Job) {
		close(entered)
		<-release
	})
	q.offer(Job{Phase: PhaseAnalyzing})
	<-entered
	closed := make(chan struct{})
	go func() {
		q.close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("close returned while a step was being sent")
	default:
	}
	close(release)
	<-closed
}
