package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/redis/go-redis/v9"
)

func deliveryRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("SILO_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SILO_TEST_REDIS_URL required for the mounted delivery record tests")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("SILO_TEST_REDIS_URL does not answer: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func uniqueDeliverySessionID(t *testing.T) string {
	return t.Name() + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// A Cast receiver pulls HLS through the proxy with no progress reports of its
// own. Each segment it fetches must reach the API's idle sweep as delivery.
func TestMountedProxyRecordsTranscodeDeliveryForTheIdleSweep(t *testing.T) {
	rdb := deliveryRedisTestClient(t)
	const secret = "proxy-delivery-secret"
	sessionID := uniqueDeliverySessionID(t)

	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = io.WriteString(w, "segment-bytes")
	}))
	t.Cleanup(node.Close)
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:            sessionID,
		PlayMethod:           "transcode",
		TranscodeNode:        node.URL,
		TranscodeTransportID: "transport-1",
		UserID:               7,
		ProfileID:            "profile-1",
		MediaFileID:          42,
	}, secret, time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	srv := newSocketProxyServer(t, secret, nil)
	srv.tracker = nodesessions.NewTracker(rdb, "http://proxy", "proxy", "proxy")
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)

	got := socketProxyRequest(t, client, http.MethodGet, server.URL+"/stream/transcode/"+token+"/segment/000.ts", nil)
	if got.status != http.StatusOK {
		t.Fatalf("segment status = %d, want 200", got.status)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		recent, err := nodesessions.RecentDeliveries(context.Background(), rdb, []string{sessionID})
		if err != nil {
			t.Fatalf("read deliveries: %v", err)
		}
		if _, ok := recent[sessionID]; ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("segment delivery never reached the idle sweep's reader")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// HEAD is a capability preflight, not playback, so it must not keep a session
// alive.
func TestMountedProxyDoesNotRecordDeliveryForHead(t *testing.T) {
	rdb := deliveryRedisTestClient(t)
	const secret = "proxy-delivery-head-secret"
	sessionID := uniqueDeliverySessionID(t)

	path := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(path, []byte("media-bytes"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:   sessionID,
		MediaPath:   path,
		PlayMethod:  "direct",
		UserID:      7,
		ProfileID:   "profile-1",
		MediaFileID: 42,
	}, secret, time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	srv := newSocketProxyServer(t, secret, nil)
	srv.tracker = nodesessions.NewTracker(rdb, "http://proxy", "proxy", "proxy")
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)

	got := socketProxyRequest(t, client, http.MethodHead, server.URL+"/stream/direct/"+token, nil)
	if got.status != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", got.status)
	}
	// HEAD never schedules a write, so there is nothing to wait for.
	recent, err := nodesessions.RecentDeliveries(context.Background(), rdb, []string{sessionID})
	if err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	if len(recent) != 0 {
		t.Fatalf("HEAD recorded delivery %v", recent)
	}
}

// A client retrying segments the node no longer has must not keep its session
// alive: only a successful relay counts as delivery.
func TestMountedProxyDoesNotRecordDeliveryForFailedSegment(t *testing.T) {
	rdb := deliveryRedisTestClient(t)
	const secret = "proxy-delivery-failed-secret"
	sessionID := uniqueDeliverySessionID(t)

	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(node.Close)
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:            sessionID,
		PlayMethod:           "transcode",
		TranscodeNode:        node.URL,
		TranscodeTransportID: "transport-1",
		UserID:               7,
		ProfileID:            "profile-1",
		MediaFileID:          42,
	}, secret, time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	srv := newSocketProxyServer(t, secret, nil)
	srv.tracker = nodesessions.NewTracker(rdb, "http://proxy", "proxy", "proxy")
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)

	got := socketProxyRequest(t, client, http.MethodGet, server.URL+"/stream/transcode/"+token+"/segment/000.ts", nil)
	if got.status != http.StatusNotFound {
		t.Fatalf("segment status = %d, want the node's 404", got.status)
	}
	// A failed relay never schedules a write, so there is nothing to wait for.
	recent, err := nodesessions.RecentDeliveries(context.Background(), rdb, []string{sessionID})
	if err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	if len(recent) != 0 {
		t.Fatalf("failed segment recorded delivery %v", recent)
	}
}
