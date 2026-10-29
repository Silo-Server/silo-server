package trickplay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type executionReadFailure struct{ fakeSettings }

func (s executionReadFailure) Get(ctx context.Context, key string) (string, error) {
	if key == ExecutionSetting {
		return "", errors.New("settings temporarily unavailable")
	}
	return s.fakeSettings.Get(ctx, key)
}

func TestNodeExtractorDoesNotRunLocallyWhenExecutionSettingIsUnreadable(t *testing.T) {
	local := &countingExtractor{}
	settings := executionReadFailure{fakeSettings{ExecutionSetting: ExecutionTranscodeNodesOnly}}
	_, err := NewNodeExtractor(local, nil, settings).Extract(t.Context(), nil, nodeRequest())
	if !errors.Is(err, errSettingsUnreadable) || local.calls != 0 {
		t.Fatalf("error=%v local calls=%d", err, local.calls)
	}
}

func TestNodeExtractorSkipsCapabilitiesRejectedByLatestHealthCheck(t *testing.T) {
	for _, mode := range []string{ExecutionPreferTranscodeNodes, ExecutionTranscodeNodesOnly} {
		for _, tc := range []struct {
			name       string
			advertised *string
			stored     *string
			eligible   bool
		}{
			{"not checked yet", nil, new("sha256:stored"), true},
			{"same hash", new("sha256:stored"), new("sha256:stored"), true},
			{"no advertised hash", new(""), new("sha256:stored"), false},
			{"different hash", new("sha256:new"), new("sha256:stored"), false},
			{"missing stored hash", new("sha256:new"), nil, false},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				node, calls := fakeNode(t, http.StatusOK, mediasample.Result{Decoder: "node"})
				node.AdvertisedCapabilitiesHash, node.CapabilitiesHash = tc.advertised, tc.stored
				local := &countingExtractor{}
				result, err := NewNodeExtractor(local, fixedNodes{node}, fakeSettings{ExecutionSetting: mode, jwtSecretSetting: "secret"}).Extract(t.Context(), nil, nodeRequest())
				if tc.eligible {
					if err != nil || result.Decoder != "node" || calls.Load() != 1 || local.calls != 0 {
						t.Fatalf("eligible node: result=%+v error=%v node calls=%d local calls=%d", result, err, calls.Load(), local.calls)
					}
					return
				}
				if calls.Load() != 0 {
					t.Fatalf("stale node received %d requests", calls.Load())
				}
				if mode == ExecutionPreferTranscodeNodes {
					if err != nil || local.calls != 1 {
						t.Fatalf("fallback: error=%v local calls=%d", err, local.calls)
					}
				} else if !errors.Is(err, errNoNode) || local.calls != 0 {
					t.Fatalf("nodes only: error=%v local calls=%d", err, local.calls)
				}
			})
		}
	}
}

func TestRemoteGenerationDoesNotRequireAPIMediaMount(t *testing.T) {
	for _, mode := range []string{ExecutionPreferTranscodeNodes, ExecutionTranscodeNodesOnly} {
		t.Run(mode, func(t *testing.T) {
			node, calls := fakeNode(t, http.StatusOK, mediasample.Result{Decoder: "software", SheetTileHeight: 126, Sheets: []mediasample.Sheet{{Index: 0, Thumbnails: 1, JPEG: []byte("jpeg")}}})
			local := &countingExtractor{}
			settings := fakeSettings{ExecutionSetting: mode, jwtSecretSetting: "secret"}
			extractor := NewNodeExtractor(local, fixedNodes{node}, settings)
			queue := newFakeQueue()
			service := newService(queue, &fakeStore{}, settings, extractor, "api")
			job := testJob(1, 10)
			job.FilePath = filepath.Join(t.TempDir(), "only-readable-on-node.mkv")
			service.process(t.Context(), job)
			if calls.Load() != 1 || local.calls != 0 || len(queue.published) != 1 || len(queue.finished) != 0 {
				t.Fatalf("node calls=%d local calls=%d published=%d finished=%v", calls.Load(), local.calls, len(queue.published), queue.finished)
			}
		})
	}
}

func TestLocalExtractorReportsUnreadableInput(t *testing.T) {
	req := nodeRequest()
	req.Input = filepath.Join(t.TempDir(), "missing.mkv")
	extractor := NewLocalExtractor(fakeSettings{hwAccelSetting: "none"})
	extractor.exec = func(_ context.Context, _ string, _ []string, _ io.Reader, _, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "No such file or directory\n")
		return errors.New("exit status 1")
	}
	_, err := extractor.Extract(t.Context(), nil, req)
	if _, ok := errors.AsType[*inputError](err); !ok {
		t.Fatalf("error=%v, want inputError", err)
	}
}

func TestNodeExtractorFallsBackAfterTransientSamplingFailure(t *testing.T) {
	for _, reason := range []string{"timeout", "killed", "unsupported", "capabilities"} {
		t.Run(reason, func(t *testing.T) {
			node, calls := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: reason})
			local := &countingExtractor{}
			result, err := NewNodeExtractor(local, fixedNodes{node}, fakeSettings{ExecutionSetting: ExecutionPreferTranscodeNodes, jwtSecretSetting: "secret"}).Extract(t.Context(), nil, nodeRequest())
			if err != nil || result.Decoder != "local" || calls.Load() != 1 || local.calls != 1 {
				t.Fatalf("result=%+v error=%v node calls=%d local calls=%d", result, err, calls.Load(), local.calls)
			}
		})
	}
}

func TestNodeExtractorTriesAllNodesAfterTransientSamplingFailure(t *testing.T) {
	first, firstCalls := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "timeout"})
	second, secondCalls := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "killed"})
	local := &countingExtractor{}
	_, err := NewNodeExtractor(local, fixedNodes{first, second}, fakeSettings{ExecutionSetting: ExecutionTranscodeNodesOnly, jwtSecretSetting: "secret"}).Extract(t.Context(), nil, nodeRequest())
	failure, ok := errors.AsType[*ExtractError](err)
	if !ok || failure.Reason != "killed" || errors.Is(err, errNoNode) || firstCalls.Load() != 1 || secondCalls.Load() != 1 || local.calls != 0 {
		t.Fatalf("error=%v node calls=%d/%d local calls=%d", err, firstCalls.Load(), secondCalls.Load(), local.calls)
	}
}

// droppingNode accepts /trickplay/extract and closes the connection without
// answering, as a node that dies mid-request does.
func droppingNode(t *testing.T) (*nodepool.Node, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(server.Close)
	return &nodepool.Node{Name: server.URL, URL: server.URL, Enabled: true, Healthy: true, Capabilities: trickplayCapabilities}, calls
}

func TestNodeExtractorFailureClassification(t *testing.T) {
	failed := finishCall{Failed, 0}
	noNode := finishCall{Released, time.Minute}
	for name, tc := range map[string]struct {
		nodes func(t *testing.T) fixedNodes
		want  finishCall
	}{
		"every node busy or unavailable": {func(t *testing.T) fixedNodes {
			busy, _ := fakeNode(t, http.StatusServiceUnavailable, ExtractError{Reason: NodeBusyReason})
			draining, _ := fakeNode(t, http.StatusServiceUnavailable, ExtractError{Reason: NodeUnavailableReason})
			return fixedNodes{busy, draining}
		}, noNode},
		"node dies mid-request": {func(t *testing.T) fixedNodes {
			dead, _ := droppingNode(t)
			return fixedNodes{dead}
		}, noNode},
		"node answers a server error": {func(t *testing.T) fixedNodes {
			broken, _ := fakeNode(t, http.StatusInternalServerError, ExtractError{Reason: "failed"})
			return fixedNodes{broken}
		}, noNode},
		"every node ran the file and failed": {func(t *testing.T) fixedNodes {
			first, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "failed", Message: "empty"})
			second, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "failed", Message: "empty"})
			return fixedNodes{first, second}
		}, failed},
		"node cannot list its capabilities": {func(t *testing.T) fixedNodes {
			broken, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "capabilities"})
			return fixedNodes{broken}
		}, noNode},
		"one node failed the file, another died": {func(t *testing.T) fixedNodes {
			failed, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "failed"})
			dead, _ := droppingNode(t)
			return fixedNodes{failed, dead}
		}, failed},
		"one node failed the file, another was busy": {func(t *testing.T) fixedNodes {
			failed, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "timeout"})
			busy, _ := fakeNode(t, http.StatusServiceUnavailable, ExtractError{Reason: NodeBusyReason})
			return fixedNodes{failed, busy}
		}, failed},
		"every node lacks a capability the file needs": {func(t *testing.T) fixedNodes {
			first, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "unsupported", Message: "configured FFmpeg lacks the required zscale filter"})
			second, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "unsupported", Message: "configured FFmpeg lacks the required zscale filter"})
			return fixedNodes{first, second}
		}, failed},
		"one node lacks a capability, another refuses the path": {func(t *testing.T) fixedNodes {
			lacking, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "unsupported"})
			refusing, _ := fakeNode(t, http.StatusBadRequest, nil)
			return fixedNodes{lacking, refusing}
		}, failed},
		"one node lacks a capability, another was busy": {func(t *testing.T) fixedNodes {
			lacking, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "unsupported"})
			busy, _ := fakeNode(t, http.StatusServiceUnavailable, ExtractError{Reason: NodeBusyReason})
			return fixedNodes{lacking, busy}
		}, noNode},
		"one node lacks a capability, another is at its job cap": {func(t *testing.T) fixedNodes {
			lacking, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "unsupported"})
			full, _ := fakeNode(t, http.StatusOK, mediasample.Result{Decoder: "node"})
			full.MaxJobs, full.ActiveJobs = new(1), 1
			return fixedNodes{lacking, full}
		}, noNode},
		"one node lacks a capability, another failed the file": {func(t *testing.T) fixedNodes {
			lacking, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "unsupported"})
			failed, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "failed"})
			return fixedNodes{lacking, failed}
		}, failed},
		"one node cannot read the input, another was busy": {func(t *testing.T) fixedNodes {
			blind, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: InputUnreadableReason})
			busy, _ := fakeNode(t, http.StatusServiceUnavailable, ExtractError{Reason: NodeBusyReason})
			return fixedNodes{blind, busy}
		}, noNode},
		"one node cannot read the input, another refuses the path": {func(t *testing.T) fixedNodes {
			blind, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: InputUnreadableReason})
			refusing, _ := fakeNode(t, http.StatusBadRequest, nil)
			return fixedNodes{blind, refusing}
		}, noNode},
		"one node cannot read the input, another failed the file": {func(t *testing.T) fixedNodes {
			blind, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: InputUnreadableReason})
			failed, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "failed"})
			return fixedNodes{blind, failed}
		}, failed},
		"one node failed the file, a healthy node refuses it": {func(t *testing.T) fixedNodes {
			failed, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "failed"})
			// A node without the library's mount rejects the input path.
			refusing, _ := fakeNode(t, http.StatusBadRequest, nil)
			return fixedNodes{failed, refusing}
		}, failed},
	} {
		t.Run(name, func(t *testing.T) {
			settings := fakeSettings{ExecutionSetting: ExecutionTranscodeNodesOnly, jwtSecretSetting: "secret"}
			extractor := NewNodeExtractor(&countingExtractor{}, tc.nodes(t), settings)
			queue := newFakeQueue()
			service := newService(queue, &fakeStore{}, settings, extractor, "api")
			service.logger = slog.New(slog.DiscardHandler)
			service.process(t.Context(), testJob(1, 10))
			if got := queue.finished[1]; got != tc.want {
				t.Fatalf("finished %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestInputUnreadableReadsTheLastAttempt(t *testing.T) {
	run := func(tails ...string) error {
		failure := &mediasample.Error{Reason: mediasample.ReasonExit}
		for _, tail := range tails {
			failure.Attempts = append(failure.Attempts, mediasample.AttemptError{Reason: mediasample.ReasonExit, StderrTail: tail})
		}
		return failure
	}
	// A VAAPI probe logs a missing driver on the hardware attempt.
	if InputUnreadable(run("dlopen of /usr/lib/dri/iHD_drv_video.so failed: No such file or directory", "Invalid data found when processing input")) {
		t.Fatal("a hardware attempt's driver log counted as an unreadable input")
	}
	if !InputUnreadable(run("Permission denied", "Error opening input files: Permission denied")) {
		t.Fatal("an input the software attempt cannot open was not reported")
	}
	// A software attempt refused before ffmpeg starts leaves no log.
	if !InputUnreadable(run("Error opening input: Permission denied", "")) {
		t.Fatal("an input the hardware attempt cannot open was not reported when software never ran")
	}
}

func TestNodeExtractorKeepsTheLastNodeAnswerWhenNoNodeRan(t *testing.T) {
	node, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: InputUnreadableReason, Message: "Permission denied"})
	settings := fakeSettings{ExecutionSetting: ExecutionTranscodeNodesOnly, jwtSecretSetting: "secret"}
	_, err := NewNodeExtractor(&countingExtractor{}, fixedNodes{node}, settings).Extract(t.Context(), nil, nodeRequest())
	if !errors.Is(err, errNoNode) || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("error=%v, want errNoNode naming the node's answer", err)
	}
}
