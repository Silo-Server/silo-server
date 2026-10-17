package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workersource"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type renewalNodes struct{ node workercontrolcore.Node }

func (n renewalNodes) ByID(context.Context, int) (workercontrolcore.Node, bool, error) {
	return n.node, true, nil
}
func (n renewalNodes) ByURL(context.Context, string) (workercontrolcore.Node, bool, error) {
	return n.node, true, nil
}

type renewalSourceAuthority struct{ *workersource.Authority }

func (a renewalSourceAuthority) Release(ctx context.Context, node int, work string, generation uint64) error {
	return a.RevokeWork(ctx, node, work, generation)
}

func TestSourceRenewalSurvivesOriginalTTLAndRequiresNodeProof(t *testing.T) {
	corePublic, corePrivate, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodePrivate, _ := ed25519.GenerateKey(rand.Reader)
	core := httptest.NewUnstartedServer(nil)
	defer core.Close()
	origin := "http://" + core.Listener.Addr().String()
	store := workercontrolcore.NewMemoryStore()
	var now atomic.Int64
	base := time.Now()
	now.Store(base.UnixNano())
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	verifier := &workeridentity.Verifier{Origin: origin, Domain: workeridentity.DomainSourceRequest, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return nodePublic, nil }}
	var service *workercontrolcore.Service
	source, err := workersource.New(workersource.Options{Origin: origin, Store: workersource.NewMemoryStore(), Now: func() time.Time { return time.Unix(0, now.Load()) }, PathApprover: func(context.Context, string, string) error { return nil }, Active: func(ctx context.Context, node int, work string, generation uint64) bool {
		active, err := store.IsActive(ctx, node, work, generation)
		return err == nil && active
	}, VerifyRequest: verifier.Verify})
	if err != nil {
		t.Fatal(err)
	}
	service, err = workercontrolcore.New(workercontrolcore.Options{Origin: origin, AllowHTTP: true, PrivateKey: corePrivate, Nodes: renewalNodes{workercontrolcore.Node{ID: 17, URL: "http://worker.example.test", PublicKey: nodePublic, Enabled: true}}, Store: store, Sources: renewalSourceAuthority{source}, SourceHandler: source.Handler(), ExecutionConfig: func(context.Context, int) (workerwire.ExecutionConfig, error) {
		return workerwire.ExecutionConfig{Revision: "r1", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: "epoch"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	core.Config.Handler = service.Handler()
	core.Start()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 17, WorkID: "work", Method: "POST", Path: "/transcode/start", Body: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), 17, "work", a.Generation); err != nil {
		t.Fatal(err)
	}
	descriptor, err := source.Acquire(t.Context(), 17, "work", a.Generation, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	a.Sources = map[string]workerwire.Source{"source:" + descriptor.Ref.ID: descriptor}
	r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: origin, CorePublicKey: corePublic, NodePrivateKey: nodePrivate, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(context.Background()) }()
	if err = r.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	handles, err := r.openSources(a)
	if err != nil {
		t.Fatal(err)
	}
	h := handles["source:"+descriptor.Ref.ID]
	for _, seconds := range []int{46, 130, 199} {
		now.Store(base.Add(time.Duration(seconds) * time.Second).UnixNano())
		if err = r.refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		r.renewals.renewDue(t.Context(), base.Add(time.Duration(seconds)*time.Second))
		if !r.workAllowed("work", a.Generation) {
			t.Fatal("successful renewal revoked work")
		}
		response, err := http.Get(h.URL())
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != "0123456789" {
			t.Fatalf("read at %ds: status=%d body=%q err=%v", seconds, response.StatusCode, body, err)
		}
		if h.Descriptor().Grant != descriptor.Grant || h.Identity() != descriptor.Ref.ID+":"+descriptor.Ref.Revision {
			t.Fatal("renewal changed stable handle")
		}
	}
	if err = store.Stop(t.Context(), 17, "work"); err != nil {
		t.Fatal(err)
	}
	r.renewals.renewDue(t.Context(), base.Add(245*time.Second))
	if r.workAllowed("work", a.Generation) || r.inputs.Allowed(h.URL()) {
		t.Fatal("denied renewal did not fence live inputs")
	}
	if _, err = r.stageSources(a); err == nil {
		t.Fatal("failed lease reopened from stale recipe")
	}
}

func TestSourceRenewalRejectsChangedDescriptorAndGrantScope(t *testing.T) {
	for _, change := range []string{"id", "revision", "kind", "origin", "url", "grant", "etag", "size", "node", "work", "generation", "envelope-node", "envelope-work", "envelope-generation", "signature", "expired"} {
		t.Run(change, func(t *testing.T) {
			corePublic, coreKey, _ := ed25519.GenerateKey(rand.Reader)
			_, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
			var original workerwire.Source
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Header.Get("X-Silo-Source-Grant") != original.Grant {
					t.Error("renewal missing exact grant header")
				}
				next := original
				grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainSourceGrant, Audience: "http://worker.example.test", NodeID: 17, WorkID: original.WorkID, Generation: original.Generation, ExpiresAt: time.Now().Add(time.Minute).Unix()}
				switch change {
				case "id":
					next.Ref.ID = "changed"
				case "revision":
					next.Ref.Revision = "changed"
				case "kind":
					next.Ref.Kind = "font"
				case "origin":
					next.Origin = "https://elsewhere.example"
				case "url":
					next.URL += "changed"
				case "grant":
					next.Grant += "changed"
				case "etag":
					next.ETag = "changed"
				case "size":
					next.Size++
				case "node":
					next.NodeID++
				case "work":
					next.WorkID = "other"
				case "generation":
					next.Generation++
				case "envelope-work":
					grant.WorkID = "other"
				case "envelope-node":
					grant.NodeID++
				case "envelope-generation":
					grant.Generation++
				case "expired":
					grant.ExpiresAt = time.Now().Add(-time.Minute).Unix()
				}
				grant.Body, _ = json.Marshal(next)
				key := coreKey
				if change == "signature" {
					_, key, _ = ed25519.GenerateKey(rand.Reader)
				}
				encoded, err := workeridentity.EncodeGrant(grant, key)
				if err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"grant": encoded})
			}))
			defer core.Close()
			r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: corePublic, NodePrivateKey: nodeKey, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(context.Background()) }()
			if err = r.authority.SetNonce("test-nonce"); err != nil {
				t.Fatal(err)
			}
			if err = r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
				t.Fatal(err)
			}
			a := raceAssignment(4)
			original = a.Sources["source:media"]
			original.Origin = core.URL
			original.URL = core.URL + "/api/internal/workers/17/sources/grant"
			a.Sources["source:media"] = original
			h, err := r.openSources(a)
			if err != nil {
				t.Fatal(err)
			}
			r.renewals.renewDue(t.Context(), time.Now().Add(46*time.Second))
			if r.workAllowed("work", 4) || r.inputs.Allowed(h["source:media"].URL()) {
				t.Fatal("changed signed descriptor renewed source")
			}
		})
	}
}

func TestSourceRenewalCancellationAndUnavailableDeadline(t *testing.T) {
	corePublic, _, _ := ed25519.GenerateKey(rand.Reader)
	_, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	started := make(chan struct{}, 1)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		started <- struct{}{}
		<-request.Context().Done()
	}))
	defer core.Close()
	r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: corePublic, NodePrivateKey: nodeKey, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(context.Background()) }()
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
		t.Fatal(err)
	}
	a := raceAssignment(4)
	source := a.Sources["source:media"]
	source.Origin = core.URL
	source.URL = core.URL + "/source"
	a.Sources["source:media"] = source
	if _, err := r.openSources(a); err != nil {
		t.Fatal(err)
	}
	// Keep control authority alive while only the source renewal is unavailable.
	// Its two-second timeout must not race the independent two-second lease.
	stopSnapshots, snapshotsDone := make(chan struct{}), make(chan struct{})
	snapshotErrors := make(chan error, 1)
	go func() {
		defer close(snapshotsDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for sequence := uint64(2); ; sequence++ {
			select {
			case <-stopSnapshots:
				return
			case <-ticker.C:
				if err := r.authority.Apply(raceSnapshot(r, sequence, 4, 2000), time.Now()); err != nil {
					snapshotErrors <- err
					return
				}
			}
		}
	}()
	defer func() { close(stopSnapshots); <-snapshotsDone }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r.renewals.renewDue(ctx, time.Now().Add(46*time.Second))
	if r.renewals.blocked("work", 4) {
		t.Fatal("caller cancellation permanently fenced healthy lease")
	}
	done := make(chan struct{})
	go func() { r.renewals.renewDue(t.Context(), time.Now().Add(46*time.Second)); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("renewal request not started")
	}
	select {
	case <-done:
	case <-time.After(sourceRenewTimeout + time.Second):
		t.Fatal("unavailable renewal exceeded bounded timeout")
	}
	if r.renewals.blocked("work", 4) {
		t.Fatal("unavailable renewal permanently fenced healthy signed work")
	}
	select {
	case err := <-snapshotErrors:
		t.Fatalf("control authority refresh: %v", err)
	default:
	}
}

func TestSourceRenewalFailureCannotRevokeNewerCandidate(t *testing.T) {
	r := raceRuntime(t)
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	snapshot := raceSnapshot(r, 1, 4, 2000)
	snapshot.Pending = map[string]uint64{"work": 5}
	if err := r.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	old, err := r.openSources(raceAssignment(4))
	if err != nil {
		t.Fatal(err)
	}
	next, err := r.stageSources(raceAssignment(5))
	if err != nil {
		t.Fatal(err)
	}
	r.renewals.mu.Lock()
	entry := r.renewals.entries[old["source:media"]]
	r.renewals.mu.Unlock()
	r.renewals.fail(entry)
	if r.inputs.Allowed(old["source:media"].URL()) || r.workAllowed("work", 4) {
		t.Fatal("old lease survived renewal failure")
	}
	if !r.inputs.Allowed(next["source:media"].URL()) || !r.workAllowed("work", 5) {
		t.Fatal("old renewal failure revoked new candidate")
	}
	r.publishStaged("work", 5)
	if !r.inputs.Allowed(next["source:media"].URL()) {
		t.Fatal("new candidate lost on publication")
	}
}

func TestSourceRenewalPrunesReleasedHandlesAndBoundsRegistry(t *testing.T) {
	r := raceRuntime(t)
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
		t.Fatal(err)
	}
	a := raceAssignment(4)
	for range maxSourceRenewals + 4 {
		h, err := r.stageSources(a)
		if err != nil {
			t.Fatal(err)
		}
		r.discardStaged("work", 4)
		if r.inputs.Allowed(h["source:media"].URL()) {
			t.Fatal("discarded candidate stayed active")
		}
	}
	r.renewals.prune()
	r.renewals.mu.Lock()
	size := len(r.renewals.entries)
	r.renewals.mu.Unlock()
	if size != 0 {
		t.Fatalf("released handle renewal metadata leaked: %d", size)
	}
}

func TestPreparedOutputDropsSourcesAndRetainsOnlyOwnGeneration(t *testing.T) {
	r := raceRuntime(t)
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	snapshot := raceSnapshot(r, 1, 4, 2000)
	snapshot.Pending = map[string]uint64{"work": 5}
	if err := r.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	old, err := r.stageSources(raceAssignment(4))
	if err != nil {
		t.Fatal(err)
	}
	r.completeSourceWork("work", 4, old)
	if !r.workAllowed("work", 4) || r.inputs.Allowed(old["source:media"].URL()) {
		t.Fatal("prepared output lost execution or retained input authority")
	}
	r.mu.Lock()
	generation := r.generations["work"]
	sources := len(r.handles["work"])
	r.mu.Unlock()
	if generation != 4 || sources != 0 {
		t.Fatalf("output generation=%d sources=%d", generation, sources)
	}
	next, err := r.openSources(raceAssignment(5))
	if err != nil {
		t.Fatal(err)
	}
	r.completeSourceWork("work", 4, old)
	if !r.inputs.Allowed(next["source:media"].URL()) {
		t.Fatal("old completion retired newer generation")
	}
	r.mu.Lock()
	generation = r.generations["work"]
	r.mu.Unlock()
	if generation != 5 {
		t.Fatal("old completion rewound newer output generation")
	}
	r.renewals.mu.Lock()
	entries := len(r.renewals.entries)
	r.renewals.mu.Unlock()
	if entries != 1 {
		t.Fatalf("renewals=%d want newer source only", entries)
	}
}
