//go:build unix

package workercontrol

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workersource"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type coldJobRecipes struct {
	mu    sync.RWMutex
	cards map[string]playback.RecipeCard
}

func (s *coldJobRecipes) Get(_ context.Context, work string) (*playback.RecipeCard, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	card, ok := s.cards[work]
	return &card, ok
}
func (s *coldJobRecipes) Delete(_ context.Context, work string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cards, work)
	return nil
}
func (s *coldJobRecipes) RecipeVersion(ctx context.Context, work string) (*playback.RecipeCard, string, error) {
	card, ok := s.Get(ctx, work)
	if !ok {
		return nil, "", nil
	}
	return card, "fixture-publication:" + work, nil
}
func (s *coldJobRecipes) DeleteVersion(_ context.Context, work, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version != "fixture-publication:"+work {
		return workercontrolcore.ErrFenced
	}
	delete(s.cards, work)
	return nil
}
func (s *coldJobRecipes) RevokeNode(_ context.Context, _ string) error { return nil }

// This follows a real signed grant through Runtime.Handler and the cold
// reconstruction path while a different transport of that playback holds the
// operator's only slot. The executable only implements stdin lifetime; it does
// not encode media or use a GPU.
//
//nolint:funlen,gocyclo // One process-lifetime scenario correlates cold reconstruction, signed playback identity, replacement, and exact producer cleanup.
func TestColdOutputReconstructionRetainsSignedPlaybackJob(t *testing.T) {
	corePublic, corePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nodePublic, nodePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core := httptest.NewUnstartedServer(nil)
	worker := httptest.NewUnstartedServer(nil)
	t.Cleanup(core.Close)
	t.Cleanup(worker.Close)
	coreURL := "http://" + core.Listener.Addr().String()
	nodeURL := "http://" + worker.Listener.Addr().String()
	const nodeID = 17
	const parent = "server-owned-parent"
	const oldWork = "native-initial-transport"
	const coldWork = "native-cold-seek-transport"
	const outputSecret = "test-cold-job-secret"
	epoch := workeridentity.OutputEpoch(outputSecret)
	sourcePath := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(sourcePath, []byte("fixture source"), 0600); err != nil {
		t.Fatal(err)
	}
	recipes := &coldJobRecipes{cards: map[string]playback.RecipeCard{}}
	for _, work := range []string{oldWork, coldWork} {
		card := playback.NewRecipeCard(42, "profile", 7, nodeURL, playback.TranscodeOpts{
			SessionID: parent, TranscodeTransportID: work, InputPath: sourcePath,
			SourceVideoCodec: "h264", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
			SegmentDuration: 2, TotalDuration: 12,
		})
		card.OriginCredential = streamtoken.OriginCredential{OriginLoginSessionID: "original-login"}
		card.OriginalStartedAt = time.Now()
		card.RoutingExecutionNodeID = nodeID
		recipes.cards[work] = card
	}
	store := workercontrolcore.NewMemoryStore()
	var service *workercontrolcore.Service
	verifier := &workeridentity.Verifier{Origin: coreURL, Domain: workeridentity.DomainSourceRequest,
		AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return nodePublic, nil }}
	source, err := workersource.New(workersource.Options{
		Origin: coreURL, Store: workersource.NewMemoryStore(),
		PathApprover: func(context.Context, string, string) error { return nil },
		Active: func(ctx context.Context, node int, work string, generation uint64) bool {
			return service.WorkActive(ctx, node, work, generation)
		},
		VerifyRequest: verifier.Verify,
	})
	if err != nil {
		t.Fatal(err)
	}
	expires := jwt.NewNumericDate(time.Now().Add(time.Hour))
	service, err = workercontrolcore.New(workercontrolcore.Options{
		Origin: coreURL, AllowHTTP: true, PrivateKey: corePrivate,
		Nodes: stopNodes{workercontrolcore.Node{ID: nodeID, URL: nodeURL, PublicKey: nodePublic, Enabled: true, AllowHTTP: true}},
		Store: store, Sources: renewalSourceAuthority{source}, SourceHandler: source.Handler(),
		RecipeStore: recipes, OutputAuthorities: workercontrolcore.NewMemoryOutputAuthorities(),
		VerifyToken: func(ctx context.Context, token string) (*streamtoken.Claims, error) {
			card, ok := recipes.Get(ctx, token)
			if !ok {
				return nil, workercontrolcore.ErrDenied
			}
			claims := card.ToClaims()
			claims.ExpiresAt = expires
			return &claims, nil
		},
		ExecutionConfig: func(context.Context, int) (workerwire.ExecutionConfig, error) {
			return workerwire.ExecutionConfig{Revision: "cold-job", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: epoch}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	core.Config.Handler = service.Handler()
	core.Start()
	program := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf '%s\\n' \"$$\" > producer.pid\nwhile IFS= read -r line; do\n [ \"$line\" = q ] && exit 0\ndone\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(t.Context(), Options{
		Binding: workeridentity.Binding{Version: 1, NodeID: nodeID, NodeURL: nodeURL, CoreURL: coreURL, CorePublicKey: corePublic, NodePrivateKey: nodePrivate, AllowHTTP: true},
		Scratch: t.TempDir(), FFmpeg: program, HWAccel: "none", MaxJobs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	worker.Config.Handler = runtime.Handler()
	worker.Start()
	client := &http.Client{Transport: service.WrapTransport(http.DefaultTransport), Timeout: 5 * time.Second}
	start := func(work, job string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"session_id": work, "input_path": sourcePath, "source_video_codec": "h264", "target_codec_video": "h264", "target_codec_audio": "aac", "segment_duration": 2, "total_duration": 12})
		ctx := nodehttp.WithPlaybackJob(t.Context(), job)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, nodeURL+"/transcode/start", bytes.NewReader(body))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	jobs := func() int32 {
		t.Helper()
		resp, err := http.Get(nodeURL + "/api/v1/health")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var health transcodenode.HealthResponse
		if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
			t.Fatal(err)
		}
		return health.ActiveJobs
	}
	producer := func(work string) int {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for time.Now().Before(deadline) {
			paths, err := filepath.Glob(filepath.Join(runtime.options.Scratch, work+"*", "producer.pid"))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err == nil && pid > 0 && syscall.Kill(pid, 0) == nil {
					return pid
				}
			}
			<-ticker.C
		}
		t.Fatal("producer did not become live")
		return 0
	}
	stop := func(work string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodDelete, nodeURL+"/transcode/"+work, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("stop status=%d", resp.StatusCode)
		}
	}
	if status := start(oldWork, parent); status != http.StatusAccepted {
		t.Fatalf("initial start status=%d", status)
	}
	if jobs() != 1 {
		t.Fatal("predecessor not retained")
	}
	oldPID := producer(oldWork)
	if err := os.Rename(program, program+".saved"); err != nil {
		t.Fatal(err)
	}
	failedStatus := start("failed-candidate", parent)
	if err := os.Rename(program+".saved", program); err != nil {
		t.Fatal(err)
	}
	if failedStatus != http.StatusInternalServerError || jobs() != 1 || syscall.Kill(oldPID, 0) != nil {
		t.Fatalf("failed candidate disturbed original producer: status=%d", failedStatus)
	}
	if _, exists, err := store.Get(t.Context(), nodeID, coldWork); err != nil || exists {
		t.Fatal("cold successor was already assigned")
	}
	issued, err := service.IssueOutputGrant(t.Context(), coldWork, nodeURL, outputSecret)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(issued.Grant, workeridentity.DomainProxyOutputGrant, nodeURL, corePublic, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	proxyKey, err := workeridentity.OutputPrivateKey(outputSecret, grant)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, nodeURL+"/transcode/"+coldWork+"/master.m3u8", nil)
		req.Header.Set("X-Silo-Stream-Token", issued.Grant)
		if err := workeridentity.Sign(req, nodeURL, workeridentity.DomainProxyOutputRequest, proxyKey, nodeID, true); err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "#EXTM3U") {
			t.Fatalf("cold/cached output attempt %d status=%d body=%s", attempt, resp.StatusCode, body)
		}
	}
	coldPID := producer(coldWork)
	if jobs() != 2 || syscall.Kill(oldPID, 0) != nil {
		t.Fatal("cold successor did not coexist with healthy predecessor")
	}
	if status := start("unrelated-play", "another-parent"); status != http.StatusServiceUnavailable {
		t.Fatalf("cold grouping released independent capacity: %d", status)
	}
	stop(oldWork)
	if jobs() != 1 || syscall.Kill(oldPID, 0) != syscall.ESRCH || syscall.Kill(coldPID, 0) != nil {
		t.Fatal("old stop removed successor or retained predecessor")
	}
	if status := start("still-unrelated", "another-parent"); status != http.StatusServiceUnavailable {
		t.Fatal("old stop released successor's job")
	}
	stop(coldWork)
	if syscall.Kill(coldPID, 0) != syscall.ESRCH {
		t.Fatal("final stop retained successor process")
	}
	if status := start("next-play", "another-parent"); status != http.StatusAccepted {
		t.Fatalf("final stop retained capacity: %d", status)
	}
}
