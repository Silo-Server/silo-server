package trickplay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type plannedNodes struct {
	*nodepool.Planner
	*nodepool.TranscodePool
}

func TestNodeExtractorReleasesCapacityAfterFailure(t *testing.T) {
	for _, failure := range []struct {
		name   string
		status int
		body   ExtractError
	}{
		{"busy", http.StatusServiceUnavailable, ExtractError{Reason: NodeBusyReason}},
		{"permanent", http.StatusUnprocessableEntity, ExtractError{Reason: "invalid_data", Permanent: true}},
	} {
		t.Run(failure.name, func(t *testing.T) {
			node, _ := fakeNode(t, failure.status, failure.body)
			node.MaxJobs = new(1)
			planner := plannerForNodes(node)
			extractor := NewNodeExtractor(&countingExtractor{}, planner, fakeSettings{ExecutionSetting: ExecutionTranscodeNodesOnly}, nodeauthtest.NodeKey("secret"))
			if _, err := extractor.Extract(t.Context(), nil, nodeRequest()); err == nil {
				t.Fatal("failed request returned success")
			}
			reserved, release := planner.ReserveTranscodeWork("download-after-failure")
			defer release()
			if reserved == nil {
				t.Fatal("failed request retained capacity")
			}
		})
	}
}

func TestNodeExtractorReleasesCapacityOnCancellation(t *testing.T) {
	started, finishServer := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-finishServer:
		}
	}))
	t.Cleanup(server.Close)
	defer close(finishServer)
	node := &nodepool.Node{URL: server.URL, Enabled: true, Healthy: true, MaxJobs: new(1), Capabilities: trickplayCapabilities}
	planner := plannerForNodes(node)
	extractor := NewNodeExtractor(&countingExtractor{}, planner, fakeSettings{ExecutionSetting: ExecutionTranscodeNodesOnly}, nodeauthtest.NodeKey("secret"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := extractor.Extract(ctx, nil, nodeRequest())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("remote extraction did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled request error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request did not finish")
	}
	reserved, release := planner.ReserveTranscodeWork("download-after-cancellation")
	defer release()
	if reserved == nil {
		t.Fatal("canceled request retained capacity")
	}
}

func plannerForNodes(nodes ...*nodepool.Node) plannedNodes {
	pool := nodepooltest.NewTranscodePool()
	pool.SetNodes(nodes)
	return plannedNodes{nodepool.NewPlanner(nil, pool), pool}
}

func TestInFlightTrickplayReservesCapacityUntilRequestEnds(t *testing.T) {
	started, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseRequest := func() { once.Do(func() { close(unblock) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-unblock
		_ = json.NewEncoder(w).Encode(mediasample.Result{Decoder: "node"})
	}))
	t.Cleanup(server.Close)
	defer releaseRequest()
	node := &nodepool.Node{URL: server.URL, Enabled: true, Healthy: true, MaxJobs: new(1), Capabilities: trickplayCapabilities}
	planner := plannerForNodes(node)
	extractor := NewNodeExtractor(&countingExtractor{}, planner, fakeSettings{ExecutionSetting: ExecutionTranscodeNodesOnly}, nodeauthtest.NodeKey("secret"))
	done := make(chan error, 1)
	go func() {
		_, err := extractor.Extract(t.Context(), nil, nodeRequest())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("remote extraction did not start")
	}
	reserved, release := planner.ReserveTranscodeWork("download-while-extracting")
	release()
	if reserved != nil {
		t.Fatal("in-flight trickplay did not consume the node's capacity")
	}
	// Once health includes this extraction, its provisional reservation must
	// stop counting. One reported job on a two-slot node leaves one slot.
	updated := *node
	updated.MaxJobs = new(2)
	planner.SetNodes([]*nodepool.Node{&updated})
	planner.ApplyHealth(node.ID, node.URL, true, 1, 0, "", nil, nil, time.Now())
	reserved, release = planner.ReserveTranscodeWork("download-after-health-update")
	release()
	if reserved == nil {
		t.Fatal("health-reported extraction and its reservation counted twice")
	}
	releaseRequest()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote extraction did not finish")
	}
	reserved, release = planner.ReserveTranscodeWork("download-after-extracting")
	defer release()
	if reserved == nil {
		t.Fatal("completed trickplay did not release capacity")
	}
}
