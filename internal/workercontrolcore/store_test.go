package workercontrolcore

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestAssignmentAdmissionStopAndLateComplete(t *testing.T) {
	s := NewMemoryStore()
	a, err := s.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "session", Method: "POST", Path: "/transcode/start"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Generation != 1 {
		t.Fatalf("generation=%d", a.Generation)
	}
	active, err := s.IsActive(t.Context(), 7, "session", 1)
	if err != nil || !active {
		t.Fatalf("pending admission cannot mint scoped source: %v %v", active, err)
	}
	if _, ok, err := s.Get(t.Context(), 7, "session"); err != nil || ok {
		t.Fatalf("pending assignment exposed for reconstruction: %v %v", ok, err)
	}
	snapshot, err := s.Active(t.Context(), 7)
	if err != nil || len(snapshot) != 0 {
		t.Fatalf("pending assignment exposed in snapshot: %v %v", snapshot, err)
	}
	if err = s.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(t.Context(), 7, "session", a.Generation); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.Active(t.Context(), 7)
	if err != nil || snapshot["session"] != 1 {
		t.Fatalf("ready snapshot=%v %v", snapshot, err)
	}
	if err = s.Stop(t.Context(), 7, "session"); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(t.Context(), a); !errors.Is(err, ErrFenced) {
		t.Fatalf("late complete=%v", err)
	}
	active, err = s.IsActive(t.Context(), 7, "session", 1)
	if err != nil || active {
		t.Fatalf("stopped source remained admitted: %v %v", active, err)
	}
	next, err := s.Begin(t.Context(), a)
	if err != nil || next.Generation <= a.Generation+1 {
		t.Fatalf("generation reused after stop: %+v %v", next, err)
	}
	if err = s.Complete(t.Context(), a); !errors.Is(err, ErrFenced) {
		t.Fatalf("old generation replaced latest: %v", err)
	}
}

func TestAssignmentStoreCopiesAndNodeIsolation(t *testing.T) {
	s := NewMemoryStore()
	a, err := s.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "work", Body: []byte(`{"input_path":"source:opaque"}`), Sources: map[string]workerwire.Source{"source:opaque": {NodeID: 7}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	a.Sources["source:opaque"] = workerwire.Source{NodeID: 9}
	got, ok, err := s.Get(t.Context(), 7, "work")
	if err != nil || !ok || got.Sources["source:opaque"].NodeID != 7 {
		t.Fatalf("caller altered persisted assignment: %+v %v", got, err)
	}
	if _, ok, err = s.Get(t.Context(), 8, "work"); err != nil || ok {
		t.Fatalf("cross-node assignment readable: %v %v", ok, err)
	}
}

func TestCandidateFailurePreservesHealthyLiveAndStopFencesBoth(t *testing.T) {
	s := NewMemoryStore()
	ctx := t.Context()
	a, err := s.Begin(ctx, workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "session", Body: []byte(`{"quality":"old"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, 7, "session", a.Generation); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.Begin(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Body = []byte(`{"quality":"new"}`)
	if err = s.Complete(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	live, ok, err := s.Get(ctx, 7, "session")
	if err != nil || !ok || live.Generation != a.Generation {
		t.Fatal("candidate replaced healthy live prematurely")
	}
	active, _ := s.Active(ctx, 7)
	pending, _ := s.Pending(ctx, 7)
	if active["session"] != a.Generation || pending["session"] != candidate.Generation {
		t.Fatalf("active=%v pending=%v", active, pending)
	}
	if err = s.Cancel(ctx, 7, "session", candidate.Generation); err != nil {
		t.Fatal(err)
	}
	live, ok, err = s.Get(ctx, 7, "session")
	if err != nil || !ok || live.Generation != a.Generation {
		t.Fatal("failed candidate killed healthy live")
	}
	next, err := s.Begin(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err = s.Stop(ctx, 7, "session"); err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, 7, "session", next.Generation); !errors.Is(err, ErrFenced) {
		t.Fatalf("late activate=%v", err)
	}
	for _, gen := range []uint64{a.Generation, next.Generation} {
		active, err := s.IsActive(ctx, 7, "session", gen)
		if err != nil || active {
			t.Fatal("stopped live or candidate remains admitted")
		}
	}
}

func TestDelayedStopCannotKillNewerGeneration(t *testing.T) {
	s := NewMemoryStore()
	ctx := t.Context()
	old, err := s.Begin(ctx, workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, 7, "work", old.Generation); err != nil {
		t.Fatal(err)
	}
	if err = s.StopIfGeneration(ctx, 7, "work", old.Generation); err != nil {
		t.Fatal(err)
	}
	newer, err := s.Begin(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(ctx, 7, "work", newer.Generation); err != nil {
		t.Fatal(err)
	}
	if err = s.StopIfGeneration(ctx, 7, "work", old.Generation); !errors.Is(err, ErrFenced) {
		t.Fatal("delayed stop accepted")
	}
	live, ok, err := s.Get(ctx, 7, "work")
	if err != nil || !ok || live.Generation != newer.Generation {
		t.Fatal("delayed stop killed new work")
	}
}
