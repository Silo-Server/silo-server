package trickplay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type fixedNodes []*nodepool.Node

func (n fixedNodes) ReserveTranscodeWorkWith(workID string, eligible func(*nodepool.Node) bool) (*nodepool.Node, func()) {
	return plannerForNodes(n...).ReserveTranscodeWorkWith(workID, eligible)
}

var trickplayCapabilities = []byte(`{"transport_features":["prepared_tracks_v1","trickplay_extract_v1"]}`)

// fakeNode answers /trickplay/extract with status and body, counting calls.
func fakeNode(t *testing.T, status int, body any) (*nodepool.Node, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/trickplay/extract" || r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req mediasample.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Attempts) != 2 || !req.Attempts[0].Hardware {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	return &nodepool.Node{Name: server.URL, URL: server.URL, Enabled: true, Healthy: true, Capabilities: trickplayCapabilities}, calls
}

type countingExtractor struct{ calls int }

func (c *countingExtractor) Extract(context.Context, *Job, mediasample.Request) (mediasample.Result, error) {
	c.calls++
	return mediasample.Result{Decoder: "local"}, nil
}

func nodeRequest() mediasample.Request {
	return mediasample.Request{Input: "/m.mkv", Samples: &mediasample.Samples{Seconds: []float64{5}},
		Sheets: &mediasample.SheetsOutput{TileWidth: 64, TileHeight: 36, Columns: 1, Rows: 1, Quality: 80}}
}

func TestNodeExtractorModes(t *testing.T) {
	ok := mediasample.Result{Decoder: "hardware:vaapi", SheetTileHeight: 284, Sheets: []mediasample.Sheet{{Index: 0, Thumbnails: 1, JPEG: []byte("jpeg")}}}
	settings := func(mode string) fakeSettings {
		return fakeSettings{ExecutionSetting: mode, jwtSecretSetting: "secret"}
	}

	t.Run("local", func(t *testing.T) {
		node, calls := fakeNode(t, http.StatusOK, ok)
		local := &countingExtractor{}
		result, err := NewNodeExtractor(local, fixedNodes{node}, settings(ExecutionLocal)).Extract(t.Context(), nil, nodeRequest())
		if err != nil || result.Decoder != "local" || calls.Load() != 0 {
			t.Fatalf("%+v %v calls %d", result, err, calls.Load())
		}
	})

	t.Run("busy node moves on", func(t *testing.T) {
		busy, busyCalls := fakeNode(t, http.StatusServiceUnavailable, ExtractError{Reason: NodeBusyReason})
		free, freeCalls := fakeNode(t, http.StatusOK, ok)
		local := &countingExtractor{}
		result, err := NewNodeExtractor(local, fixedNodes{busy, free}, settings(ExecutionTranscodeNodesOnly)).Extract(t.Context(), nil, nodeRequest())
		if err != nil || result.Decoder != "hardware:vaapi" || result.SheetTileHeight != 284 || string(result.Sheets[0].JPEG) != "jpeg" || freeCalls.Load() != 1 || busyCalls.Load() > 1 || local.calls != 0 {
			t.Fatalf("%+v %v", result, err)
		}
	})

	t.Run("prefer falls back to local", func(t *testing.T) {
		busy, _ := fakeNode(t, http.StatusServiceUnavailable, ExtractError{Reason: NodeBusyReason})
		dead := &nodepool.Node{Name: "dead", URL: "http://127.0.0.1:1", Enabled: true, Healthy: true, Capabilities: trickplayCapabilities}
		local := &countingExtractor{}
		result, err := NewNodeExtractor(local, fixedNodes{busy, dead}, settings(ExecutionPreferTranscodeNodes)).Extract(t.Context(), nil, nodeRequest())
		if err != nil || result.Decoder != "local" || local.calls != 1 {
			t.Fatalf("%+v %v", result, err)
		}
	})

	t.Run("nodes only without a node", func(t *testing.T) {
		old := &nodepool.Node{Name: "old", URL: "http://127.0.0.1:1", Enabled: true, Healthy: true, Capabilities: []byte(`{"transport_features":["prepared_tracks_v1"]}`)}
		sick := &nodepool.Node{Name: "sick", URL: "http://127.0.0.1:1", Enabled: true, Healthy: false, Capabilities: trickplayCapabilities}
		off := &nodepool.Node{Name: "off", URL: "http://127.0.0.1:1", Enabled: false, Healthy: true, Capabilities: trickplayCapabilities}
		local := &countingExtractor{}
		_, err := NewNodeExtractor(local, fixedNodes{old, sick, off}, settings(ExecutionTranscodeNodesOnly)).Extract(t.Context(), nil, nodeRequest())
		if !errors.Is(err, errNoNode) || local.calls != 0 {
			t.Fatalf("err %v local %d", err, local.calls)
		}
	})

	t.Run("a failure on a node is final", func(t *testing.T) {
		broken, _ := fakeNode(t, http.StatusUnprocessableEntity, ExtractError{Reason: "invalid_data", Permanent: true, Message: "moov atom not found"})
		other, otherCalls := fakeNode(t, http.StatusOK, ok)
		_, err := NewNodeExtractor(&countingExtractor{}, fixedNodes{broken, other}, settings(ExecutionTranscodeNodesOnly)).Extract(t.Context(), nil, nodeRequest())
		failure, isNode := errors.AsType[*ExtractError](err)
		if !isNode || !failure.Permanent || failure.Reason != "invalid_data" || otherCalls.Load() != 0 {
			t.Fatalf("err %v", err)
		}
		if outcome, _ := (&Service{}).classify(t.Context(), err); outcome != Unusable {
			t.Fatalf("a permanent node failure classifies as %v", outcome)
		}
	})
}

func TestMakesTrickplay(t *testing.T) {
	for report, want := range map[string]bool{
		`{"transport_features":["trickplay_extract_v1"]}`: true,
		`{"transport_features":["prepared_tracks_v1"]}`:   false,
		`{}`: false,
		``:   false,
		`{`:  false,
	} {
		if got := makesTrickplay([]byte(report)); got != want {
			t.Errorf("%q: %t", report, got)
		}
	}
}

func TestNodeExtractorRetriesWorkersWithoutDisplayGeometry(t *testing.T) {
	for _, mode := range []string{ExecutionPreferTranscodeNodes, ExecutionTranscodeNodesOnly} {
		t.Run(mode, func(t *testing.T) {
			node, calls := fakeNode(t, http.StatusOK, mediasample.Result{
				Decoder: "software", Sheets: []mediasample.Sheet{{Index: 0, Thumbnails: 1, JPEG: []byte("old geometry")}},
			})
			local := &countingExtractor{}
			req := nodeRequest()
			req.Sheets.UseInputAspect = true
			settings := fakeSettings{ExecutionSetting: mode, jwtSecretSetting: "secret"}
			result, err := NewNodeExtractor(local, fixedNodes{node}, settings).Extract(t.Context(), nil, req)
			if calls.Load() != 1 {
				t.Fatalf("node calls %d", calls.Load())
			}
			if mode == ExecutionPreferTranscodeNodes {
				if err != nil || result.Decoder != "local" || local.calls != 1 {
					t.Fatalf("fallback = %+v, %v; local calls %d", result, err, local.calls)
				}
			} else if !errors.Is(err, errNoNode) || local.calls != 0 {
				t.Fatalf("nodes only = %v; local calls %d", err, local.calls)
			}
		})
	}
}
