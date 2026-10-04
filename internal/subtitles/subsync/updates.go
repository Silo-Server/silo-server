package subsync

import "sync"

// updateQueue delivers a run's progress to players off the sync worker: a
// slow player connection or an unreachable event bus must not hold a worker
// slot. Only the newest step not yet sent is kept. close waits for the step
// being sent, so the outcome that follows it reaches players last.
type updateQueue struct {
	pending chan Job
	done    chan struct{}
	once    sync.Once
}

func newUpdateQueue(send func(Job)) *updateQueue {
	q := &updateQueue{pending: make(chan Job, 1), done: make(chan struct{})}
	go func() {
		defer close(q.done)
		for job := range q.pending {
			send(job)
		}
	}()
	return q
}

// offer queues job, replacing a step not yet sent. Only the run's worker
// calls it, and never after close.
func (q *updateQueue) offer(job Job) {
	for {
		select {
		case q.pending <- job:
			return
		default:
			select {
			case <-q.pending:
			default:
			}
		}
	}
}

// close drops a step not yet sent and waits for the one being sent.
func (q *updateQueue) close() {
	q.once.Do(func() {
		select {
		case <-q.pending:
		default:
		}
		close(q.pending)
		<-q.done
	})
}
