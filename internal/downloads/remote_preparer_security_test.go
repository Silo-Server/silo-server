package downloads

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestNodeAwarePreparerCapabilityProbeDoesNotForwardJWTOnRedirect(t *testing.T) {
	const jwt = "cluster-jwt-secret"
	var redirectTargetHits atomic.Int32
	var redirectTargetAuthorization atomic.Value
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHits.Add(1)
		redirectTargetAuthorization.Store(r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{})
	}))
	defer redirectTarget.Close()

	var sourceAuthorization atomic.Value
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceAuthorization.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, redirectTarget.URL+"/hw-capabilities", http.StatusFound)
	}))
	defer source.Close()

	var injectedRedirectPolicyCalls atomic.Int32
	injectedClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		injectedRedirectPolicyCalls.Add(1)
		return nil
	}}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = jwt
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })
	preparer.probeClient = injectedClient

	_, err := preparer.toneMapCapabilitiesForNode(context.Background(), source.URL)
	if got, want := sourceAuthorization.Load(), "Bearer "+jwt; got != want {
		t.Fatalf("source Authorization = %#v, want %q", got, want)
	}
	if got := redirectTargetHits.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, Authorization = %#v; want no redirect request", got, redirectTargetAuthorization.Load())
	}
	if got := injectedRedirectPolicyCalls.Load(); got != 0 {
		t.Fatalf("injected redirect policy calls = %d, want secure per-probe policy", got)
	}
	if err == nil || !strings.Contains(err.Error(), "transcode node returned 302") {
		t.Fatalf("redirect response error = %v, want transcode node returned 302", err)
	}
	if injectedClient.CheckRedirect == nil {
		t.Fatal("capability probe cleared the injected client's redirect policy")
	}
	if err := injectedClient.CheckRedirect(nil, nil); err != nil {
		t.Fatalf("injected client redirect policy after probe = %v, want original policy", err)
	}
	if got := injectedRedirectPolicyCalls.Load(); got != 1 {
		t.Fatalf("injected redirect policy calls after direct invocation = %d, want 1", got)
	}
}

func TestNodeAwarePreparerCapabilityProbeRejectsOversizedResponse(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resolved":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer remote.Close()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "cluster-jwt-secret"
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })

	_, err := preparer.toneMapCapabilitiesForNode(context.Background(), remote.URL)
	if err == nil || !strings.Contains(err.Error(), "capability response exceeds") {
		t.Fatalf("oversized response error = %v, want bounded-response error", err)
	}
}

func TestNodeAwarePreparerCoalescesConcurrentColdCapabilityProbes(t *testing.T) {
	const callers = 8
	payload, err := json.Marshal(playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int32
	firstStarted := make(chan struct{})
	duplicateStarted := make(chan struct{})
	release := make(chan struct{})
	var firstOnce sync.Once
	var duplicateOnce sync.Once
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		count := requests.Add(1)
		firstOnce.Do(func() { close(firstStarted) })
		if count > 1 {
			duplicateOnce.Do(func() { close(duplicateStarted) })
		}
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(payload))),
			Request:    request,
		}, nil
	})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "cluster-jwt-secret"
	preparer := NewNodeAwarePreparer(nil, nil, func() *config.Config { return cfg })
	preparer.probeClient = &http.Client{Transport: transport}

	type result struct {
		capabilities tonemap.Capabilities
		err          error
	}
	start := make(chan struct{})
	ready := sync.WaitGroup{}
	ready.Add(callers)
	results := make(chan result, callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			capabilities, probeErr := preparer.toneMapCapabilitiesForNode(context.Background(), "https://node.example")
			results <- result{capabilities: capabilities, err: probeErr}
		}()
	}
	ready.Wait()
	close(start)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("first capability probe did not start")
	}
	duplicate := false
	select {
	case <-duplicateStarted:
		duplicate = true
	case <-time.After(150 * time.Millisecond):
	}
	close(release)

	for range callers {
		select {
		case got := <-results:
			if got.err != nil || !got.capabilities.Supports(tonemap.ModeSoftware, tonemap.SourcePQ) {
				t.Fatalf("capability result = %#v, %v", got.capabilities, got.err)
			}
		case <-time.After(time.Second):
			t.Fatal("coalesced capability caller did not return")
		}
	}
	if duplicate || requests.Load() != 1 {
		t.Fatalf("capability HTTP requests = %d, want one coalesced probe", requests.Load())
	}
}
