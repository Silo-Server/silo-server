package workercontrol

import (
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestRetiredWorkReleasesStagingState(t *testing.T) {
	for _, lifecycle := range []string{"publish", "discard", "revoke", "prepare"} {
		t.Run(lifecycle, func(t *testing.T) {
			r := raceRuntime(t)
			if err := r.authority.SetNonce("test-nonce"); err != nil {
				t.Fatal(err)
			}
			active := make(map[string]uint64)
			for i := 0; i < 100; i++ {
				active[fmt.Sprintf("job-%d", i)] = 1
			}
			if err := r.authority.Apply(workerwire.Snapshot{Version: 1, NodeID: 17, BootID: r.bootID, Nonce: "test-nonce", Sequence: 1, Active: active, ValidForMillis: 2000}, time.Now()); err != nil {
				t.Fatal(err)
			}
			for work := range active {
				assignment := raceAssignment(1)
				assignment.WorkID = work
				source := assignment.Sources["source:media"]
				source.WorkID = work
				assignment.Sources["source:media"] = source
				handles, err := r.stageSources(assignment)
				if err != nil {
					t.Fatal(err)
				}
				switch lifecycle {
				case "publish":
					r.publishStaged(work, 1)
				case "discard":
					r.discardStaged(work, 1)
				case "revoke":
					r.revoked(work, 1)
				case "prepare":
					r.completeSourceWork(work, 1, handles)
				}
				r.mu.Lock()
				_, retained := r.staged[work]
				r.mu.Unlock()
				if retained {
					t.Fatal("retired generation retained its staging map")
				}
				r.revoked(work, 1)
			}
			r.renewals.prune()
			r.mu.Lock()
			defer r.mu.Unlock()
			if len(r.generations) != 0 || len(r.handles) != 0 || len(r.staged) != 0 {
				t.Fatal("retired jobs retained runtime state")
			}
		})
	}
}

func TestStagedCleanupPreservesOtherGeneration(t *testing.T) {
	r := raceRuntime(t)
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	snapshot := raceSnapshot(r, 1, 1, 2000)
	snapshot.Pending = map[string]uint64{"work": 2}
	if err := r.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	var next map[string]*workerinput.Handle
	for _, generation := range []uint64{1, 2} {
		handles, err := r.stageSources(raceAssignment(generation))
		if err != nil {
			t.Fatal(err)
		}
		if generation == 2 {
			next = handles
		}
	}
	r.discardStaged("work", 1)
	if !r.inputs.Allowed(next["source:media"].URL()) {
		t.Fatal("discarding an older generation closed its successor")
	}
	r.publishStaged("work", 2)
	old, err := r.stageSources(raceAssignment(1))
	if err != nil {
		t.Fatal(err)
	}
	r.publishStaged("work", 1)
	if r.inputs.Allowed(old["source:media"].URL()) || !r.inputs.Allowed(next["source:media"].URL()) {
		t.Fatal("outdated publication did not preserve the successor")
	}
	r.mu.Lock()
	retained := len(r.staged)
	r.mu.Unlock()
	if retained != 0 {
		t.Fatal("outdated publication retained its staging map")
	}
}
