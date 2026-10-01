package trickplay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
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
	if !errors.Is(err, errNoNode) || firstCalls.Load() != 1 || secondCalls.Load() != 1 || local.calls != 0 {
		t.Fatalf("error=%v node calls=%d/%d local calls=%d", err, firstCalls.Load(), secondCalls.Load(), local.calls)
	}
}
