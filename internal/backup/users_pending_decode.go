package backup

import (
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// pendingPreparer is a data type whose documents decode ahead of their
// comparison (see userdata.PendingFunc).
type pendingPreparer interface {
	PreparePending(data json.RawMessage) (userdata.PendingFunc, error)
}

// pendingDecodes decodes the documents of the data types that can be
// decoded ahead (pendingPreparer), in the order the preview
// compares them, on a bounded number of goroutines, while the
// comparison queries the one connection. At most that many decoded
// documents wait at once; a document whose profile the preview does not
// compare is dropped when the comparison moves past it.
type pendingDecodes struct {
	jobs  []decodeJob
	index map[decodeKey]int
	// next is the first job the comparison has not taken or passed.
	next int
	// window holds a token per job started and not yet taken or passed.
	window chan struct{}
	done   chan struct{}
}

type decodeKey struct {
	entry  *ProfileEntry
	typeID string
}

type decodeJob struct {
	prepare pendingPreparer
	data    json.RawMessage
	out     chan decodeResult
}

type decodeResult struct {
	compare userdata.PendingFunc
	err     error
}

// startPendingDecodes starts decoding docs' documents on up to workers
// goroutines; stop ends it.
func startPendingDecodes(docs []UserDocument, workers int) *pendingDecodes {
	d := &pendingDecodes{index: map[decodeKey]int{}, window: make(chan struct{}, max(workers, 1)), done: make(chan struct{})}
	for i := range docs {
		for j := range docs[i].Profiles {
			e := &docs[i].Profiles[j]
			for _, t := range userDataTypes {
				prepare, ok := t.(pendingPreparer)
				data, has := e.Data[t.ID()]
				if !ok || !has {
					continue
				}
				d.index[decodeKey{e, t.ID()}] = len(d.jobs)
				d.jobs = append(d.jobs, decodeJob{prepare: prepare, data: data, out: make(chan decodeResult, 1)})
			}
		}
	}
	go d.produce()
	return d
}

func (d *pendingDecodes) produce() {
	for i := range d.jobs {
		select {
		case d.window <- struct{}{}:
		case <-d.done:
			return
		}
		go func(job decodeJob) {
			compare, err := job.prepare.PreparePending(job.data)
			job.out <- decodeResult{compare: compare, err: err}
		}(d.jobs[i])
	}
}

// take returns the decoded document of entry's data type, or its decode
// error; false when it was not decoded ahead (the caller then runs Pending
// itself).
func (d *pendingDecodes) take(entry *ProfileEntry, typeID string) (decodeResult, bool) {
	i, found := d.index[decodeKey{entry, typeID}]
	if !found || i < d.next {
		return decodeResult{}, false
	}
	// Jobs start in order, so each before i has started or will: drop
	// those of profiles the comparison skipped.
	for ; d.next < i; d.next++ {
		<-d.jobs[d.next].out
		<-d.window
	}
	r := <-d.jobs[i].out
	<-d.window
	d.next = i + 1
	return r, true
}

// stop ends the decoding; jobs already started finish into their buffers.
func (d *pendingDecodes) stop() { close(d.done) }
