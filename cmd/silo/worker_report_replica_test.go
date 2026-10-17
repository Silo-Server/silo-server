package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func (f *reportFixture) replica(t *testing.T) *workerReportSink {
	t.Helper()
	sink := &workerReportSink{rdb: f.rdb, nodes: f.sink.nodes, recipes: f.sink.recipes, assignments: f.store, service: f.sink.service}
	t.Cleanup(func() {
		for _, publisher := range sink.publishers {
			_ = publisher.Leave(context.Background())
		}
	})
	return sink
}

func reportJSON(t *testing.T, batch workerwire.Report) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *reportFixture) session() workerwire.SessionReport {
	return workerwire.SessionReport{WorkID: f.assignment.WorkID, Generation: f.assignment.Generation, Info: nodesessions.SessionInfo{SessionID: f.card.SessionID, Type: "transcode"}}
}

func TestWorkerReportReplicaRemovesStoppedSession(t *testing.T) {
	f := newReportFixture(t)
	replica := f.replica(t)
	batch := workerwire.Report{BootID: "boot-a", Sequence: 1, Sessions: []workerwire.SessionReport{f.session()}}
	if err := f.accept(t, batch); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.load(t, f.card.SessionID); !ok {
		t.Fatal("first replica did not publish the active session")
	}
	if err := replica.accept(t.Context(), f.node.ID, reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: 2})); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.load(t, f.card.SessionID); ok {
		t.Fatal("fresh replica retained a stopped session")
	}
	if err := f.accept(t, batch); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.load(t, f.card.SessionID); ok {
		t.Fatal("delayed report restored the stopped session")
	}
}

func TestWorkerReportReplicaPublishesCompleteTelemetry(t *testing.T) {
	f := newReportFixture(t)
	replica := f.replica(t)
	other := f.activate(t, workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: "report:" + uuid.NewString(), Path: "/transcode/start"})
	card := f.card
	card.SessionID = "canonical-" + uuid.NewString()
	if err := f.sink.recipes.Put(t.Context(), other.WorkID, card); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.sink.recipes.Delete(context.Background(), other.WorkID) })
	first := f.observations(100)
	second := f.observations(100)
	second.Scopes[0] = workerwire.ObservationScope{WorkID: other.WorkID, Generation: other.Generation, SessionID: card.SessionID}
	second.Snapshot.Sessions[0].SessionID = card.SessionID
	for _, item := range []struct {
		sink *workerReportSink
		seq  uint64
		obs  *workerwire.ObservationReport
	}{{f.sink, 1, first}, {replica, 2, second}, {f.sink, 3, f.observations(100)}} {
		if err := item.sink.accept(t.Context(), f.node.ID, reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: item.seq, Observations: item.obs})); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := f.sink.publishers[f.node.ID].Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Sequence != 3 || len(snapshot.Sessions) != 1 || snapshot.Sessions[0].SessionID != f.card.SessionID {
		t.Fatalf("replica delta retained another replica's session: %+v", snapshot)
	}
}

type reportNodeSet []workercontrolcore.Node

func (nodes reportNodeSet) ByID(_ context.Context, id int) (workercontrolcore.Node, bool, error) {
	for _, node := range nodes {
		if node.ID == id {
			return node, true, nil
		}
	}
	return workercontrolcore.Node{}, false, nil
}

func (nodes reportNodeSet) ByURL(_ context.Context, origin string) (workercontrolcore.Node, bool, error) {
	for _, node := range nodes {
		if node.URL == origin {
			return node, true, nil
		}
	}
	return workercontrolcore.Node{}, false, nil
}

func (f *reportFixture) checkExecution(t *testing.T, check func(context.Context, workercontrolcore.Node, workerwire.Assignment) bool) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example.test", PrivateKey: key, Nodes: f.sink.nodes, Store: f.store, RecipeStore: f.sink.recipes, ExecutionActive: check})
	if err != nil {
		t.Fatal(err)
	}
	f.sink.service = func() *workercontrolcore.Service { return service }
}

func TestWorkerReportBlockedNodeDoesNotBlockAnotherNode(t *testing.T) {
	f := newReportFixture(t)
	other := newReportFixture(t)
	f.sink.nodes = reportNodeSet{f.node, other.node}
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	f.checkExecution(t, func(ctx context.Context, _ workercontrolcore.Node, _ workerwire.Assignment) bool {
		close(entered)
		select {
		case <-resume:
			return true
		case <-ctx.Done():
			return false
		}
	})
	first := reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: 1, Sessions: []workerwire.SessionReport{f.session()}})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	secondStarted, secondReceived := false, false
	go func() { firstDone <- f.sink.accept(t.Context(), f.node.ID, first) }()
	defer func() {
		release()
		<-firstDone
		if secondStarted && !secondReceived {
			<-secondDone
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first report did not reach the blocking authority check")
	}
	second := reportJSON(t, workerwire.Report{BootID: "boot-b", Sequence: 1})
	secondStarted = true
	go func() { secondDone <- f.sink.accept(t.Context(), other.node.ID, second) }()
	select {
	case err := <-secondDone:
		secondReceived = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("one node's authority check blocked an unrelated node's report")
	}
}

func TestWorkerReportExpiredLeaseCannotRemoveSuccessorSession(t *testing.T) {
	f := newReportFixture(t)
	replica := f.replica(t)
	if err := f.accept(t, workerwire.Report{BootID: "boot-a", Sequence: 1, Sessions: []workerwire.SessionReport{f.session()}}); err != nil {
		t.Fatal(err)
	}
	other := f.activate(t, workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: "background:" + uuid.NewString(), Path: "/sample"})
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	f.checkExecution(t, func(ctx context.Context, _ workercontrolcore.Node, assignment workerwire.Assignment) bool {
		if assignment.WorkID != other.WorkID {
			return true
		}
		close(entered)
		select {
		case <-resume:
			return true
		case <-ctx.Done():
			return false
		}
	})
	old := reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: 2, Sessions: []workerwire.SessionReport{{WorkID: other.WorkID, Generation: other.Generation, Info: nodesessions.SessionInfo{SessionID: other.WorkID}}}})
	oldDone := make(chan error, 1)
	oldReceived := false
	go func() { oldDone <- f.sink.accept(t.Context(), f.node.ID, old) }()
	defer func() {
		release()
		if !oldReceived {
			<-oldDone
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old report did not reach the blocking authority check")
	}
	lock := "silo:http-workers:report:" + strconv.Itoa(f.node.ID) + ":lock"
	if err := f.rdb.Del(t.Context(), lock).Err(); err != nil {
		t.Fatal(err)
	}
	if err := replica.accept(t.Context(), f.node.ID, reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: 3, Sessions: []workerwire.SessionReport{f.session()}})); err != nil {
		t.Fatal(err)
	}
	release()
	err := <-oldDone
	oldReceived = true
	if err == nil {
		t.Error("report whose lease was replaced was accepted")
	}
	if _, ok := f.load(t, f.card.SessionID); !ok {
		t.Fatal("old lease removed its successor's active session")
	}
	if _, ok := f.load(t, other.WorkID); ok {
		t.Fatal("old lease published a session after a newer report")
	}
}

type reportTelemetryTestKey struct{}
type reportTelemetryTestHook struct{ entered, resume chan struct{} }

func (h reportTelemetryTestHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h reportTelemetryTestHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}
func (h reportTelemetryTestHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		if ctx.Value(reportTelemetryTestKey{}) != nil && len(commands) > 0 && commands[0].Name() == "multi" {
			close(h.entered)
			select {
			case <-h.resume:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return next(ctx, commands)
	}
}

func TestWorkerReportExpiredLeaseCannotOverwriteSuccessorTelemetry(t *testing.T) {
	f := newReportFixture(t)
	replica := f.replica(t)
	if err := f.accept(t, workerwire.Report{BootID: "boot-a", Sequence: 1, Sessions: []workerwire.SessionReport{f.session()}, Observations: f.observations(100)}); err != nil {
		t.Fatal(err)
	}
	hook := reportTelemetryTestHook{entered: make(chan struct{}), resume: make(chan struct{})}
	f.rdb.AddHook(hook)
	release := sync.OnceFunc(func() { close(hook.resume) })
	old := reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: 2, Sessions: []workerwire.SessionReport{f.session()}, Observations: f.observations(200)})
	done := make(chan error, 1)
	received := false
	go func() {
		done <- f.sink.accept(context.WithValue(t.Context(), reportTelemetryTestKey{}, true), f.node.ID, old)
	}()
	defer func() {
		release()
		if !received {
			<-done
		}
	}()
	select {
	case <-hook.entered:
	case <-time.After(time.Second):
		t.Fatal("old report did not reach telemetry after publishing membership")
	}
	if err := f.rdb.Del(t.Context(), "silo:http-workers:report:"+strconv.Itoa(f.node.ID)+":lock").Err(); err != nil {
		t.Fatal(err)
	}
	if err := replica.accept(t.Context(), f.node.ID, reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: 3, Sessions: []workerwire.SessionReport{f.session()}, Observations: f.observations(300)})); err != nil {
		t.Fatal(err)
	}
	release()
	err := <-done
	received = true
	if err == nil {
		t.Error("old report published telemetry after its lease was replaced")
	}
	snapshot, err := replica.publishers[f.node.ID].Load(t.Context())
	if err != nil || snapshot.Sequence != 3 || len(snapshot.Sessions) != 1 || snapshot.Sessions[0].BytesAccepted != 300 {
		t.Fatalf("old report overwrote successor telemetry: snapshot=%+v error=%v", snapshot, err)
	}
}

type reportCursorTestKey struct{}
type reportCursorTestHook struct {
	key             string
	entered, resume chan struct{}
}

func (h reportCursorTestHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h reportCursorTestHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h reportCursorTestHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		err := next(ctx, command)
		if ctx.Value(reportCursorTestKey{}) != nil && command.Name() == "get" && command.Args()[1] == h.key {
			close(h.entered)
			select {
			case <-h.resume:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}
}

func TestWorkerReportExpiredLeaseCannotRollBackCursor(t *testing.T) {
	f := newReportFixture(t)
	replica := f.replica(t)
	key := "silo:http-workers:report:" + strconv.Itoa(f.node.ID)
	hook := reportCursorTestHook{key: key, entered: make(chan struct{}), resume: make(chan struct{})}
	f.rdb.AddHook(hook)
	release := sync.OnceFunc(func() { close(hook.resume) })
	old := reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: 1})
	done := make(chan error, 1)
	received := false
	go func() {
		done <- f.sink.accept(context.WithValue(t.Context(), reportCursorTestKey{}, true), f.node.ID, old)
	}()
	defer func() {
		release()
		if !received {
			<-done
		}
	}()
	select {
	case <-hook.entered:
	case <-time.After(time.Second):
		t.Fatal("old report did not read the cursor")
	}
	if err := f.rdb.Del(t.Context(), key+":lock").Err(); err != nil {
		t.Fatal(err)
	}
	if err := replica.accept(t.Context(), f.node.ID, reportJSON(t, workerwire.Report{BootID: "boot-a", Sequence: 2, Sessions: []workerwire.SessionReport{f.session()}})); err != nil {
		t.Fatal(err)
	}
	release()
	err := <-done
	received = true
	if err == nil {
		t.Error("old report was accepted after losing its cursor lease")
	}
	raw, err := f.rdb.Get(t.Context(), key).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var cursor workerReportCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	if cursor.BootID != "boot-a" || cursor.Sequence != 2 {
		t.Fatalf("old lease rolled back the successor's cursor: %+v", cursor)
	}
	if _, ok := f.load(t, f.card.SessionID); !ok {
		t.Fatal("old lease removed its successor's session")
	}
}
