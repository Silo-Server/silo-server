package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type reportNodes struct{ node workercontrolcore.Node }

func (n reportNodes) ByID(_ context.Context, id int) (workercontrolcore.Node, bool, error) {
	return n.node, id == n.node.ID, nil
}
func (n reportNodes) ByURL(_ context.Context, url string) (workercontrolcore.Node, bool, error) {
	return n.node, url == n.node.URL, nil
}

type reportFixture struct {
	sink       *workerReportSink
	store      *workercontrolcore.MemoryStore
	rdb        *redis.Client
	node       workercontrolcore.Node
	assignment workerwire.Assignment
	card       playback.RecipeCard
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	rawURL := os.Getenv("SILO_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("SILO_TEST_REDIS_URL is not set")
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	nodePublic, _, _ := ed25519.GenerateKey(rand.Reader)
	_, corePrivate, _ := ed25519.GenerateKey(rand.Reader)
	// Each fixture gets an independent node and recipe namespace in the task Redis.
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	nodeID := int(random[0])<<24 | int(random[1])<<16 | int(random[2])<<8 | int(random[3])
	nodeID = (nodeID & 0x3fffffff) + 1
	f := &reportFixture{rdb: rdb, node: workercontrolcore.Node{ID: nodeID, URL: "https://" + uuid.NewString() + ".example.test", PublicKey: nodePublic, Enabled: true}, store: workercontrolcore.NewMemoryStore()}
	f.assignment = f.activate(t, workerwire.Assignment{Version: 1, NodeID: nodeID, WorkID: "report:" + uuid.NewString(), Path: "/transcode/start"})
	f.card = playback.RecipeCard{SessionID: "canonical-" + uuid.NewString(), UserID: 41, ProfileID: "actual-profile", MediaFileID: 73, TranscodeNodeURL: f.node.URL}
	recipes := noderecipe.NewStore(rdb, time.Minute)
	if err := recipes.Put(t.Context(), f.assignment.WorkID, f.card); err != nil {
		t.Fatal(err)
	}
	service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example.test", PrivateKey: corePrivate, Nodes: reportNodes{f.node}, Store: f.store, RecipeStore: recipes})
	if err != nil {
		t.Fatal(err)
	}
	f.sink = &workerReportSink{rdb: rdb, nodes: reportNodes{f.node}, recipes: recipes, assignments: f.store, service: func() *workercontrolcore.Service { return service }}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, tracker := range f.sink.trackers {
			tracker.Cleanup(ctx)
		}
		for _, publisher := range f.sink.publishers {
			_ = publisher.Leave(ctx)
		}
		_ = recipes.Delete(ctx, f.assignment.WorkID)
		_ = rdb.Del(ctx, "silo:http-workers:report:"+strconv.Itoa(nodeID), "silo:http-workers:report:"+strconv.Itoa(nodeID)+":lock").Err()
	})
	return f
}
func (f *reportFixture) activate(t *testing.T, a workerwire.Assignment) workerwire.Assignment {
	t.Helper()
	a, err := f.store.Begin(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Activate(t.Context(), a.NodeID, a.WorkID, a.Generation); err != nil {
		t.Fatal(err)
	}
	return a
}
func (f *reportFixture) accept(t *testing.T, batch workerwire.Report) error {
	t.Helper()
	data, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	return f.sink.accept(t.Context(), f.node.ID, data)
}
func (f *reportFixture) load(t *testing.T, id string) (nodesessions.SessionInfo, bool) {
	t.Helper()
	tr := nodesessions.NewTracker(f.rdb, f.node.URL, "", "")
	raw, err := f.rdb.Get(t.Context(), "silo:sessions:"+tr.NodeHash()+":"+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nodesessions.SessionInfo{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var info nodesessions.SessionInfo
	if err = json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	return info, true
}
func (f *reportFixture) observations(bytes int64) *workerwire.ObservationReport {
	return &workerwire.ObservationReport{Scopes: []workerwire.ObservationScope{{WorkID: f.assignment.WorkID, Generation: f.assignment.Generation, SessionID: f.card.SessionID}}, Snapshot: streamtelemetry.Snapshot{PublisherID: "forged-publisher", NodeID: "forged-node", Sessions: []streamtelemetry.SessionView{{SessionID: f.card.SessionID, Subject: streamtelemetry.Subject{Kind: streamtelemetry.SubjectUser, ID: "999"}, ProfileID: "forged", MediaFileID: 999, ViewerIPs: []string{"192.0.2.17"}, DeviceIDs: []string{"forged-device"}, UserAgents: []string{"forged-agent"}, BytesAccepted: bytes, Routes: []streamtelemetry.RouteActivityView{{Method: "GET", Pattern: "/transcode/{session_id}/segment/{name}", Role: streamtelemetry.RoleViewerEgress, CapRelevant: true, BytesAccepted: bytes}}}}}}
}
func TestWorkerReportReplacesForgedOwnershipAndDropsForeignScopes(t *testing.T) {
	f := newReportFixture(t)
	forged := nodesessions.SessionInfo{SessionID: f.assignment.WorkID, AuthUserID: 999, ProfileID: "forged", MediaFileID: 999, NodeURL: "https://forged.example.test", UserID: "forged", MediaItemID: "forged", MediaTitle: "forged", Type: "transcode"}
	valid := workerwire.SessionReport{WorkID: f.assignment.WorkID, Generation: f.assignment.Generation, Info: forged}
	stale := valid
	stale.Generation++
	foreign := valid
	foreign.WorkID = "foreign-" + uuid.NewString()
	foreign.Info.SessionID = "foreign-session"
	phantom := valid
	phantom.Info.SessionID = "phantom-session"
	batch := workerwire.Report{BootID: "boot-a", Sequence: 1, Sessions: []workerwire.SessionReport{stale, foreign, phantom, valid}, Observations: f.observations(100)}
	batch.Observations.Scopes = append(batch.Observations.Scopes, workerwire.ObservationScope{WorkID: foreign.WorkID, Generation: 1, SessionID: "foreign-session"})
	batch.Observations.Snapshot.Sessions = append(batch.Observations.Snapshot.Sessions, streamtelemetry.SessionView{SessionID: "foreign-session", BytesAccepted: 999999})
	if err := f.accept(t, batch); err != nil {
		t.Fatal(err)
	}
	info, ok := f.load(t, f.card.SessionID)
	if !ok || info.AuthUserID != f.card.UserID || info.ProfileID != f.card.ProfileID || info.MediaFileID != f.card.MediaFileID || info.NodeURL != f.node.URL || info.UserID != "" || info.MediaItemID != "" || info.MediaTitle != "" {
		t.Fatalf("reported ownership was not derived from core recipe: %+v", info)
	}
	for _, id := range []string{f.assignment.WorkID, "foreign-session", "phantom-session"} {
		if _, ok := f.load(t, id); ok {
			t.Fatalf("unauthorized session %q was published", id)
		}
	}
	snapshot, err := f.sink.publishers[f.node.ID].Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sessions) != 1 || snapshot.PublisherID != "http-worker-"+strconv.Itoa(f.node.ID) {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	view := snapshot.Sessions[0]
	if view.Subject.ID != "41" || view.ProfileID != f.card.ProfileID || view.MediaFileID != 73 || len(view.ViewerIPs) != 0 || len(view.DeviceIDs) != 0 || len(view.UserAgents) != 0 || view.Routes[0].CapRelevant || view.Routes[0].Role != streamtelemetry.RoleInternalRelay {
		t.Fatalf("forged labels/cap accounting survived: %+v", view)
	}
}
func TestWorkerReportIgnoresPhantomBackgroundSessionAndStaleGeneration(t *testing.T) {
	f := newReportFixture(t)
	background := f.activate(t, workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: "probe:" + uuid.NewString(), Path: "/sample"})
	forged := workerwire.SessionReport{WorkID: background.WorkID, Generation: background.Generation, Info: nodesessions.SessionInfo{SessionID: "another-viewer-session", AuthUserID: 999, ProfileID: "forged", MediaFileID: 999}}
	if err := f.accept(t, workerwire.Report{BootID: "boot-a", Sequence: 1, Sessions: []workerwire.SessionReport{forged}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.load(t, forged.Info.SessionID); ok {
		t.Fatal("valid background work published a phantom viewer session")
	}
	next := f.activate(t, f.assignment)
	stale := workerwire.SessionReport{WorkID: f.assignment.WorkID, Generation: f.assignment.Generation, Info: nodesessions.SessionInfo{SessionID: f.card.SessionID, AuthUserID: 999}}
	o := f.observations(999999)
	if err := f.accept(t, workerwire.Report{BootID: "boot-a", Sequence: 2, Sessions: []workerwire.SessionReport{stale}, Observations: o}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.load(t, f.card.SessionID); ok {
		t.Fatal("stale generation published viewer ownership")
	}
	snapshot, err := f.sink.publishers[f.node.ID].Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sessions) != 0 {
		t.Fatal("stale generation contributed capacity telemetry")
	}
	if next.Generation <= f.assignment.Generation {
		t.Fatal("fixture did not advance generation")
	}
}
func TestWorkerReportRejectsUndeclaredObservationRoute(t *testing.T) {
	f := newReportFixture(t)
	o := f.observations(100)
	o.Snapshot.Sessions[0].Routes[0].Pattern = "/admin/private"
	if err := f.accept(t, workerwire.Report{BootID: "boot-a", Sequence: 1, Observations: o}); err == nil {
		t.Fatal("undeclared observation route accepted")
	}
	if publisher := f.sink.publishers[f.node.ID]; publisher != nil {
		snapshot, err := publisher.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Sessions) != 0 {
			t.Fatal("undeclared route published telemetry")
		}
	}
}
func TestWorkerReportCannotRollBackNewerSequence(t *testing.T) {
	f := newReportFixture(t)
	if err := f.accept(t, workerwire.Report{BootID: "boot-a", Sequence: 2, Observations: f.observations(200)}); err != nil {
		t.Fatal(err)
	}
	// A second API replica must read the same boot/sequence authority.
	replica := &workerReportSink{rdb: f.rdb, nodes: f.sink.nodes, recipes: f.sink.recipes, assignments: f.store, service: f.sink.service}
	t.Cleanup(func() {
		for _, p := range replica.publishers {
			_ = p.Leave(context.Background())
		}
	})
	submit := func(batch workerwire.Report) {
		t.Helper()
		raw, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if err = replica.accept(t.Context(), f.node.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
	submit(workerwire.Report{BootID: "boot-a", Sequence: 1, Observations: f.observations(100)})
	snapshot, err := f.sink.publishers[f.node.ID].Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Sequence != 2 || len(snapshot.Sessions) != 1 || snapshot.Sessions[0].BytesAccepted != 200 {
		t.Fatal("delayed report rolled back a newer telemetry snapshot")
	}
	submit(workerwire.Report{BootID: "boot-b", Sequence: 1, Observations: f.observations(300)})
	if err = f.accept(t, workerwire.Report{BootID: "boot-a", Sequence: 3, Observations: f.observations(100)}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = replica.publishers[f.node.ID].Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Sequence != 1 || len(snapshot.Sessions) != 1 || snapshot.Sessions[0].BytesAccepted != 300 {
		t.Fatal("retired worker boot rolled back replacement boot")
	}
}
