package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// fakeMoveStore is the shared node recipe store with a scripted API server
// behind it: a published move request rewrites the record to name movedTo,
// or the next of moves when that is set.
type fakeMoveStore struct {
	mu        sync.Mutex
	cards     map[string]playback.RecipeCard
	movedTo   string
	moves     []string
	lookupErr error
	requests  []noderecipe.MoveRequest
	reads     atomic.Int32
}

func (s *fakeMoveStore) Lookup(_ context.Context, id string) (*playback.RecipeCard, bool, error) {
	s.reads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookupErr != nil {
		return nil, false, s.lookupErr
	}
	card, ok := s.cards[id]
	if !ok {
		return nil, false, nil
	}
	return &card, true, nil
}

func (s *fakeMoveStore) Publish(_ context.Context, channel string, event cache.Event) error {
	if channel != cache.ChannelTranscodeMove || event.Type != cache.EventTranscodeMoveRequested {
		return nil
	}
	request, ok := noderecipe.DecodeMoveRequest(event.Payload)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	movedTo := s.movedTo
	if len(s.moves) > 0 {
		movedTo, s.moves = s.moves[0], s.moves[1:]
	}
	if movedTo != "" {
		card := s.cards[request.TransportID]
		card.TranscodeNodeURL = movedTo
		s.cards[request.TransportID] = card
	}
	return nil
}

func (s *fakeMoveStore) moveRequests() []noderecipe.MoveRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]noderecipe.MoveRequest(nil), s.requests...)
}

// deadNodeURL returns the URL of a listener that has been closed, so a dial
// to it is refused the way a killed node's port is.
func deadNodeURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	url := "http://" + listener.Addr().String()
	_ = listener.Close()
	return url
}

func newMoveTestProxy(t *testing.T, store *fakeMoveStore) *Server {
	t.Helper()
	server := newCompletionTestProxy(http.DefaultClient)
	server.SetTranscodeMoveFollower(store, store)
	server.moves.wait = 2 * time.Second
	server.moves.poll = 5 * time.Millisecond
	server.moves.republish = time.Second
	return server
}

func relaySegment(server *Server, claims *streamtoken.Claims) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/stream/transcode/token/segment/seg_00019.ts", nil)
	rr := httptest.NewRecorder()
	server.proxyToTranscodeNode(rr, req, claims, "/transcode/upstream-1/segment/seg_00019.ts", "token")
	return rr
}

// A Jellyfin-compatible token carries no recipe, so its transport lives in
// the shared store. When the token's node is dead, the proxy asks central to
// move the transport and relays the same request to the node the record names
// afterwards; later requests go straight there.
func TestProxyFollowsStoreBackedTranscodeOffDeadNode(t *testing.T) {
	var newNodeRequests atomic.Int32
	newNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		newNodeRequests.Add(1)
		if r.URL.Path != "/transcode/upstream-1/segment/seg_00019.ts" {
			t.Errorf("new node path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Silo-Stream-Token") != "token" {
			t.Error("new node did not receive the forwarded stream token")
		}
		_, _ = w.Write([]byte("segment from the new node"))
	}))
	defer newNode.Close()
	dead := deadNodeURL(t)
	store := &fakeMoveStore{
		cards:   map[string]playback.RecipeCard{"upstream-1": {SessionID: "upstream-1", TranscodeNodeURL: dead, SegmentDuration: 2, TargetCodecVideo: "h264"}},
		movedTo: newNode.URL,
	}
	server := newMoveTestProxy(t, store)
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: dead}

	rr := relaySegment(server, claims)
	if rr.Code != http.StatusOK || rr.Body.String() != "segment from the new node" {
		t.Fatalf("relay = %d %q, want 200 from the new node", rr.Code, rr.Body.String())
	}
	requests := store.moveRequests()
	if len(requests) != 1 || requests[0].TransportID != "upstream-1" || requests[0].NodeURL != dead {
		t.Fatalf("move requests = %+v, want one for upstream-1 off %s", requests, dead)
	}

	rr = relaySegment(server, claims)
	if rr.Code != http.StatusOK {
		t.Fatalf("second relay = %d, want 200", rr.Code)
	}
	if got := len(store.moveRequests()); got != 1 {
		t.Fatalf("second relay sent another move request (%d total)", got)
	}
	if got := newNodeRequests.Load(); got != 2 {
		t.Fatalf("new node requests = %d, want 2", got)
	}
}

// Concurrent requests that find the node dead share one wait.
func TestProxyFollowSingleFlightsConcurrentRequests(t *testing.T) {
	newNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer newNode.Close()
	dead := deadNodeURL(t)
	store := &fakeMoveStore{
		cards:   map[string]playback.RecipeCard{"upstream-1": {SessionID: "upstream-1", TranscodeNodeURL: dead}},
		movedTo: newNode.URL,
	}
	server := newMoveTestProxy(t, store)
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: dead}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if rr := relaySegment(server, claims); rr.Code != http.StatusOK {
				t.Errorf("relay = %d, want 200", rr.Code)
			}
		})
	}
	wg.Wait()
	if got := len(store.moveRequests()); got != 1 {
		t.Fatalf("move requests = %d, want 1", got)
	}
}

// A native token carries its whole recipe: the client replans on failure, so
// the proxy neither asks for a move nor waits.
func TestProxyDoesNotFollowTokenCarriedRecipe(t *testing.T) {
	dead := deadNodeURL(t)
	store := &fakeMoveStore{cards: map[string]playback.RecipeCard{}}
	server := newMoveTestProxy(t, store)
	claims := &streamtoken.Claims{
		SessionID: "native-1", PlayMethod: "transcode", TranscodeNode: dead,
		SegmentDuration: 6, TargetCodec: "h264",
	}

	rr := relaySegment(server, claims)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("relay = %d, want 502", rr.Code)
	}
	if store.reads.Load() != 0 || len(store.moveRequests()) != 0 {
		t.Fatalf("native relay consulted the move protocol: reads=%d requests=%d", store.reads.Load(), len(store.moveRequests()))
	}
}

// A record can be briefly absent while a move commits (an old node binary
// deletes it when central stops the transport there). The proxy keeps
// polling and follows the record once it is back.
func TestProxyFollowKeepsWaitingThroughMissingRecord(t *testing.T) {
	newNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("segment from the new node"))
	}))
	defer newNode.Close()
	dead := deadNodeURL(t)
	store := &fakeMoveStore{cards: map[string]playback.RecipeCard{}}
	server := newMoveTestProxy(t, store)
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: dead}

	go func() {
		// The record reappears, naming the new node, once the proxy has read
		// the gap a few times.
		for store.reads.Load() < 3 {
			time.Sleep(time.Millisecond) //nolint:forbidigo // polls the recipe store, the observable state
		}
		store.mu.Lock()
		store.cards["upstream-1"] = playback.RecipeCard{SessionID: "upstream-1", TranscodeNodeURL: newNode.URL}
		store.mu.Unlock()
	}()
	rr := relaySegment(server, claims)

	if rr.Code != http.StatusOK || rr.Body.String() != "segment from the new node" {
		t.Fatalf("relay = %d %q, want 200 from the new node", rr.Code, rr.Body.String())
	}
	if len(store.moveRequests()) != 0 {
		t.Fatal("asked to move a transport whose record was absent")
	}
}

// A transport whose record never comes back has stopped: the request fails
// once the wait ends, without asking for a move.
func TestProxyFollowGivesUpWithoutRecord(t *testing.T) {
	dead := deadNodeURL(t)
	store := &fakeMoveStore{cards: map[string]playback.RecipeCard{}}
	server := newMoveTestProxy(t, store)
	server.moves.wait = 100 * time.Millisecond
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: dead}

	rr := relaySegment(server, claims)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("relay = %d, want 502", rr.Code)
	}
	if len(store.moveRequests()) != 0 {
		t.Fatal("asked to move a transport without a record")
	}
}

// When nobody moves the transport, the request gives up after the wait and
// the client's next retry asks again.
func TestProxyFollowGivesUpWhenNothingMoves(t *testing.T) {
	dead := deadNodeURL(t)
	store := &fakeMoveStore{cards: map[string]playback.RecipeCard{"upstream-1": {SessionID: "upstream-1", TranscodeNodeURL: dead}}}
	server := newMoveTestProxy(t, store)
	server.moves.wait = 100 * time.Millisecond
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: dead}

	rr := relaySegment(server, claims)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("relay = %d, want 502", rr.Code)
	}
	if len(store.moveRequests()) == 0 {
		t.Fatal("the proxy did not ask central to move the transport")
	}
}

// A followed transport's record is re-read periodically, so a later move or a
// return to the token's node is followed too.
func TestProxyFollowRechecksRecord(t *testing.T) {
	var hits sync.Map
	node := func(name string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			count, _ := hits.LoadOrStore(name, new(atomic.Int32))
			count.(*atomic.Int32).Add(1)
			_, _ = w.Write([]byte(name))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	second, third := node("second"), node("third")
	dead := deadNodeURL(t)
	store := &fakeMoveStore{
		cards:   map[string]playback.RecipeCard{"upstream-1": {SessionID: "upstream-1", TranscodeNodeURL: dead}},
		movedTo: second.URL,
	}
	server := newMoveTestProxy(t, store)
	now := time.Now()
	server.moves.now = func() time.Time { return now }
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: dead}

	if rr := relaySegment(server, claims); rr.Body.String() != "second" {
		t.Fatalf("first relay served by %q, want second", rr.Body.String())
	}
	store.mu.Lock()
	card := store.cards["upstream-1"]
	card.TranscodeNodeURL = third.URL
	store.cards["upstream-1"] = card
	store.mu.Unlock()
	if rr := relaySegment(server, claims); rr.Body.String() != "second" {
		t.Fatalf("relay before the recheck served by %q, want second", rr.Body.String())
	}
	now = now.Add(movedTranscodeRecheck)
	if rr := relaySegment(server, claims); rr.Body.String() != "third" {
		t.Fatalf("relay after the recheck served by %q, want third", rr.Body.String())
	}
	// An unreadable store says nothing about where the transport runs.
	store.mu.Lock()
	store.lookupErr = errors.New("redis unavailable")
	store.mu.Unlock()
	now = now.Add(movedTranscodeRecheck)
	if rr := relaySegment(server, claims); rr.Body.String() != "third" {
		t.Fatalf("relay during a store outage served by %q (%d), want third", rr.Body.String(), rr.Code)
	}
}

// A request that moves past several dead nodes tells each move about the
// nodes it already could not reach, which the API can still list as healthy.
func TestProxyFollowCarriesFailedNodes(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("live"))
	}))
	defer live.Close()
	first, second := deadNodeURL(t), deadNodeURL(t)
	store := &fakeMoveStore{
		cards: map[string]playback.RecipeCard{"upstream-1": {SessionID: "upstream-1", TranscodeNodeURL: first}},
		moves: []string{second, live.URL},
	}
	server := newMoveTestProxy(t, store)
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: first}

	if rr := relaySegment(server, claims); rr.Code != http.StatusOK || rr.Body.String() != "live" {
		t.Fatalf("relay = %d %q, want 200 from the live node", rr.Code, rr.Body.String())
	}
	requests := store.moveRequests()
	if len(requests) != 2 || requests[1].NodeURL != second ||
		len(requests[1].FailedNodes) != 1 || requests[1].FailedNodes[0] != first {
		t.Fatalf("move requests = %+v, want the second to name %s and leave out %s", requests, second, first)
	}
}

// Only HLS transcode routes follow a move. A progressive remux on a transcode
// node uses the same record as its authority, not as a rebuild recipe.
func TestProxyDoesNotFollowProgressiveRemux(t *testing.T) {
	dead := deadNodeURL(t)
	store := &fakeMoveStore{cards: map[string]playback.RecipeCard{"upstream-1": {SessionID: "upstream-1", TranscodeNodeURL: dead}}}
	server := newMoveTestProxy(t, store)
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "remux", TranscodeNode: dead}

	req := httptest.NewRequest(http.MethodGet, "/stream/remux/token", nil)
	rr := httptest.NewRecorder()
	server.proxyToTranscodeNode(rr, req, claims, "/remux/upstream-1", "token")

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("relay = %d, want 502", rr.Code)
	}
	if store.reads.Load() != 0 || len(store.moveRequests()) != 0 {
		t.Fatalf("progressive remux consulted the move protocol: reads=%d requests=%d", store.reads.Load(), len(store.moveRequests()))
	}
}

// A proxy that starts after a move has nothing cached for the transport. When
// the token's node is back up, it still answers, so the proxy must not relay
// there: that node would rebuild the transport from the moved record and run
// a second FFmpeg. The first request reads the record and goes to the node it
// names.
func TestProxyFollowsMoveMadeBeforeItStarted(t *testing.T) {
	served := func(name string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(name))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	original, moved := served("original"), served("moved")
	store := &fakeMoveStore{cards: map[string]playback.RecipeCard{
		"upstream-1": {SessionID: "upstream-1", TranscodeNodeURL: moved.URL},
	}}
	server := newMoveTestProxy(t, store)
	claims := &streamtoken.Claims{SessionID: "upstream-1", PlayMethod: "transcode", TranscodeNode: original.URL}

	for range 2 {
		if rr := relaySegment(server, claims); rr.Code != http.StatusOK || rr.Body.String() != "moved" {
			t.Fatalf("relay = %d %q, want 200 from the node the record names", rr.Code, rr.Body.String())
		}
	}
	if got := store.reads.Load(); got != 1 {
		t.Fatalf("record reads = %d, want 1 within the recheck interval", got)
	}
	if len(store.moveRequests()) != 0 {
		t.Fatal("asked for a move while every node answered")
	}
}
