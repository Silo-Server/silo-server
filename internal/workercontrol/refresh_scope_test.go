package workercontrol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

// Hold a complete signed reply after the core has captured its authority set.
// Changes made after captured therefore cannot appear in this first reply.
func captureFirstRefresh(t *testing.T, r *Runtime) (<-chan struct{}, func(), *atomic.Int32) {
	t.Helper()
	base := r.client.Transport
	captured, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	var snapshots atomic.Int32
	r.client.Transport = refreshTransport(func(request *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(request)
		if err != nil || !strings.HasSuffix(request.URL.Path, "/snapshot") || snapshots.Add(1) != 1 {
			return response, err
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(bytes.NewReader(raw))
		close(captured)
		select {
		case <-release:
		case <-request.Context().Done():
		}
		return response, nil
	})
	return captured, unblock, &snapshots
}

func awaitScopeWaiter(t *testing.T, waiting <-chan struct{}) {
	t.Helper()
	select {
	case <-waiting:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("scoped caller did not join the captured snapshot")
	}
}

//nolint:funlen // Correlate a signed proof with the snapshot captured before or after that viewer's registration.
func TestOutputRefreshCoversViewerRegisteredAfterCapturedSnapshot(t *testing.T) {
	for _, mode := range []string{"proxy-proof", "prepared-token"} {
		for _, scenario := range []struct {
			name     string
			existing bool
			newWork  bool
			denied   bool
		}{
			{name: "new-viewer"},
			{name: "existing-viewer", existing: true},
			{name: "new-work", newWork: true},
			{name: "denied-viewer", denied: true},
		} {
			t.Run(mode+"/"+scenario.name, func(t *testing.T) {
				viewer := strings.Repeat("a", 64)
				var registered, admitted atomic.Bool
				registered.Store(scenario.existing)
				admitted.Store(!scenario.newWork)
				r, coreKey := newRefreshFixtureWithKey(t, func(_ http.ResponseWriter, _ *http.Request, _ refreshChallenge, reply func()) { reply() }, func(snapshot *workerwire.Snapshot) {
					if !admitted.Load() {
						snapshot.Active = map[string]uint64{}
					}
					if registered.Load() {
						snapshot.Outputs = map[string]bool{viewer: true}
					}
				})
				if err := r.config.apply(workerwire.ExecutionConfig{Revision: "setup", SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: "epoch"}); err != nil {
					t.Fatal(err)
				}
				captured, release, snapshots := captureFirstRefresh(t, r)
				initial := make(chan error, 1)
				go func() { initial <- r.refresh(t.Context()) }()
				<-captured
				registered.Store(!scenario.denied)
				admitted.Store(true)
				proxyPublic, proxyKey, _ := ed25519.GenerateKey(rand.Reader)
				claims := streamtoken.Claims{SessionID: "work", UserID: 42, TranscodeNode: r.options.Binding.NodeURL, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
				if mode == "prepared-token" {
					claims.DownloadArtifactID = "work"
				}
				inner, _ := json.Marshal(claims)
				body, _ := json.Marshal(workeridentity.OutputAuthority{AuthorityID: viewer, Epoch: "epoch", ProxyPublicKey: proxyPublic, Claims: inner})
				token, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: r.options.Binding.NodeURL, NodeID: 17, WorkID: "work", Generation: 4, ExpiresAt: time.Now().Add(time.Minute).Unix(), Body: body}, coreKey)
				if err != nil {
					t.Fatal(err)
				}
				ctx := &refreshWaitContext{Context: t.Context(), waiting: make(chan struct{})}
				done := make(chan error, 1)
				if mode == "proxy-proof" {
					request := httptest.NewRequest(http.MethodGet, r.options.Binding.NodeURL+"/transcode/work/master.m3u8", nil).WithContext(ctx)
					request.Header.Set("X-Silo-Stream-Token", token)
					if err := workeridentity.Sign(request, r.options.Binding.NodeURL, workeridentity.DomainProxyOutputRequest, proxyKey, 17, true); err != nil {
						t.Fatal(err)
					}
					go func() { done <- r.verifyCommand(request) }()
					awaitScopeWaiter(t, ctx.waiting)
				} else {
					go func() { _, err := r.verifyToken(ctx, token); done <- err }()
					// The pre-fix token path rejects a new viewer immediately.
					select {
					case <-ctx.waiting:
					case err := <-done:
						t.Fatalf("prepared viewer was rejected before refreshing: %v", err)
					case <-time.After(200 * time.Millisecond):
						t.Fatal("prepared viewer did not reach authority refresh")
					}
				}
				release()
				if err := awaitRefresh(t, initial); err != nil {
					t.Fatal(err)
				}
				err = awaitRefresh(t, done)
				if scenario.denied && err == nil {
					t.Fatal("fresh snapshot's missing viewer was granted authority")
				}
				if !scenario.denied && err != nil {
					t.Fatalf("authorized output joined a pre-registration snapshot: %v", err)
				}
				want := int32(2)
				if scenario.existing {
					want = 1
				}
				if snapshots.Load() != want || r.authority.OutputAllowed(viewer) == scenario.denied || !r.workAllowed("work", 4) {
					t.Fatalf("output scope/coalescing mismatch: snapshots=%d want=%d", snapshots.Load(), want)
				}
			})
		}
	}
}

func TestColdCoreOutputRefreshCoversWorkAdmittedAfterCapturedSnapshot(t *testing.T) {
	var admitted atomic.Bool
	r, coreKey := newRefreshFixtureWithKey(t, func(_ http.ResponseWriter, _ *http.Request, _ refreshChallenge, reply func()) { reply() }, func(snapshot *workerwire.Snapshot) {
		if !admitted.Load() {
			snapshot.Active = map[string]uint64{}
		}
	})
	captured, release, snapshots := captureFirstRefresh(t, r)
	initial := make(chan error, 1)
	go func() { initial <- r.refresh(t.Context()) }()
	<-captured
	admitted.Store(true)
	ctx := &refreshWaitContext{Context: t.Context(), waiting: make(chan struct{})}
	request := httptest.NewRequest(http.MethodGet, r.options.Binding.NodeURL+"/downloads/artifacts/work", nil).WithContext(ctx)
	if err := workeridentity.Sign(request, r.options.Binding.NodeURL, workeridentity.DomainCoreCommand, coreKey, 17, true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.verifyCommand(request) }()
	awaitScopeWaiter(t, ctx.waiting)
	release()
	if err := awaitRefresh(t, initial); err != nil {
		t.Fatal(err)
	}
	if err := awaitRefresh(t, done); err != nil {
		t.Fatalf("cold core output joined a pre-admission snapshot: %v", err)
	}
	if snapshots.Load() != 2 || r.authority.Generation("work") != 4 {
		t.Fatalf("cold output omitted a newly admitted generation: snapshots=%d", snapshots.Load())
	}
}

func TestForceReloadRequiresSnapshotSentAfterInvocation(t *testing.T) {
	var changed atomic.Bool
	r, _ := newRefreshFixtureWithKey(t, func(_ http.ResponseWriter, _ *http.Request, _ refreshChallenge, reply func()) { reply() }, func(snapshot *workerwire.Snapshot) {
		revision := "before"
		if changed.Load() {
			revision = "after"
		}
		snapshot.ExecutionConfig = &workerwire.ExecutionConfig{Revision: revision, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: revision}
	})
	captured, release, snapshots := captureFirstRefresh(t, r)
	initial := make(chan error, 1)
	go func() { initial <- r.refresh(t.Context()) }()
	<-captured
	changed.Store(true)
	ctx := &refreshWaitContext{Context: t.Context(), waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- r.config.ForceReload(ctx) }()
	awaitScopeWaiter(t, ctx.waiting)
	release()
	if err := awaitRefresh(t, initial); err != nil {
		t.Fatal(err)
	}
	if err := awaitRefresh(t, done); err != nil {
		t.Fatal(err)
	}
	if snapshots.Load() != 2 || r.config.OutputAuthorizationEpoch() != "after" {
		t.Fatalf("forced reload accepted a snapshot sent before invocation: snapshots=%d epoch=%s", snapshots.Load(), r.config.OutputAuthorizationEpoch())
	}
}
