package jellycompat

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

type remoteStartTransportFunc func(*http.Request) (*http.Response, error)

func (f remoteStartTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Use the real manifest route and actual TCP failure rather than a fabricated
// start error: the regression was the cleanup request masking safe failover.
func TestMasterManifestReplansAfterUndispatchedRemoteStart(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var deletes, starts atomic.Int32
	nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper {
		return remoteStartTransportFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.String(), deadURL+"/") {
				deletes.Add(1)
			}
			return base.RoundTrip(r)
		})
	})
	t.Cleanup(func() { nodehttp.Configure(nil) })
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transcode/start" {
			http.NotFound(w, r)
			return
		}
		starts.Add(1)
		var request transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
			SessionID: request.SessionID, ThrottleSeconds: request.ThrottleSeconds,
		})
	}))
	t.Cleanup(healthy.Close)
	handler, planner, sessionMgr, store, source := newManifestToneMapFailoverHandler(t, deadURL, false, healthy.URL)
	source.Version.HDR = false
	source.Version.VideoTracks = []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}
	file := handler.fileResolver.(testCompatFileResolver).file
	file.HDR, file.VideoTracks = false, source.Version.VideoTracks
	if err := store.Update("play-1", func(p *PlaybackSession) error {
		p.MediaSources = []PlaybackMediaSource{source}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handler.RecipeNodeStore = &stubRecipeNodeStore{}
	r := httptest.NewRequest(http.MethodGet, "/Videos/item/master.m3u8?PlaySessionId=play-1&MediaSourceId="+source.ID, nil)
	r = r.WithContext(context.WithValue(r.Context(), compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}))
	w := httptest.NewRecorder()
	handler.HandleMasterManifest(w, r)
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("manifest = %d %s, want healthy-worker redirect; DELETEs=%d", w.Code, w.Body.String(), deletes.Load())
	}
	if deletes.Load() != 0 || starts.Load() != 1 {
		t.Fatalf("unreachable DELETEs=%d healthy starts=%d, want 0/1", deletes.Load(), starts.Load())
	}
	if planner.TranscodeNodeHealthy(deadURL) {
		t.Fatal("connection-refused worker remains healthy")
	}
	upstream, err := sessionMgr.GetSession("upstream-1")
	if err != nil || upstream.TranscodeNodeURL != healthy.URL {
		t.Fatalf("upstream = %+v, err=%v, want healthy worker", upstream, err)
	}
	stored, ok := store.Get("play-1")
	if !ok || !stored.TranscodeStarted || stored.Recipe == nil || stored.Recipe.TranscodeNodeURL != healthy.URL {
		t.Fatalf("accepted healthy recipe = %+v, found=%v", stored, ok)
	}
}

func TestRemoteStartLostResponseKeepsUncertainWork(t *testing.T) {
	testRemoteStartLostResponse(t, false)
}

func TestRemoteStartConnectedAttemptThenDialErrorKeepsUncertainWork(t *testing.T) {
	testRemoteStartLostResponse(t, true)
}

func testRemoteStartLostResponse(t *testing.T, finalDialFailure bool) {
	t.Helper()
	if finalDialFailure {
		// A composed transport can return a later dial failure after the worker
		// saw the request. Retain evidence from the earlier connected attempt.
		nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper {
			return remoteStartTransportFunc(func(r *http.Request) (*http.Response, error) {
				response, err := base.RoundTrip(r)
				if err != nil && r.Method == http.MethodPost {
					return response, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
				}
				return response, err
			})
		})
		t.Cleanup(func() { nodehttp.Configure(nil) })
	}
	var starts, deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		starts.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(node.Close)
	handler, _, store := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
	if !recipePublicationUncertain(err) || starts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("lost response err=%v starts=%d DELETEs=%d, want uncertain/1/1", err, starts.Load(), deletes.Load())
	}
	if _, ok := store.Get("play-1"); !ok {
		t.Fatal("uncertain work was removed")
	}
}

type partialRemoteStartConn struct{ net.Conn }

func (c partialRemoteStartConn) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), "POST ") {
		// Send real request bytes but fail before the JSON body is complete.
		n, err := c.Conn.Write(p[:len(p)-1])
		if err != nil {
			return n, err
		}
		return n, &net.OpError{Op: "write", Net: "tcp", Err: syscall.ETIMEDOUT}
	}
	return c.Conn.Write(p)
}

func TestRemoteStartPartialWriteKeepsUncertainWork(t *testing.T) {
	var deletes atomic.Int32
	partial := make(chan int, 1)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err == nil {
			t.Error("partial-write fixture delivered a complete request")
		}
		partial <- len(body)
	}))
	t.Cleanup(node.Close)
	nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper {
		transport := base.(*http.Transport).Clone()
		dial := transport.DialContext
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return partialRemoteStartConn{conn}, nil
		}
		t.Cleanup(transport.CloseIdleConnections)
		return transport
	})
	t.Cleanup(func() { nodehttp.Configure(nil) })
	handler, _, store := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	err := handler.startRemoteTranscode(t.Context(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
	if !recipePublicationUncertain(err) || deletes.Load() != 1 {
		t.Fatalf("partial write err=%v DELETEs=%d, want uncertain/1", err, deletes.Load())
	}
	select {
	case n := <-partial:
		if n == 0 {
			t.Fatal("partial write sent no body bytes")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker never observed partial request")
	}
	if _, ok := store.Get("play-1"); !ok {
		t.Fatal("uncertain work was removed")
	}
}

func TestRemoteStartDelayedAcceptCleanupPrecedesSuccessor(t *testing.T) {
	previousTimeout := compatRemoteTranscodeStartTimeout
	compatRemoteTranscodeStartTimeout = 100 * time.Millisecond
	t.Cleanup(func() { compatRemoteTranscodeStartTimeout = previousTimeout })
	accept, accepted := make(chan struct{}), make(chan struct{})
	cleanup, cleaning := make(chan struct{}), make(chan struct{})
	releaseAccept := sync.OnceFunc(func() { close(accept) })
	releaseCleanup := sync.OnceFunc(func() { close(cleanup) })
	defer releaseAccept()
	defer releaseCleanup()
	var starts, killed atomic.Int32
	var alive atomic.Bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			close(cleaning)
			select {
			case <-cleanup:
			case <-r.Context().Done():
				return
			}
			<-accepted
			killed.Store(starts.Load())
			alive.Store(false)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if starts.Add(1) == 1 {
			<-accept
			alive.Store(true)
			close(accepted)
		} else {
			alive.Store(true)
		}
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
			SessionID: request.SessionID, ThrottleSeconds: request.ThrottleSeconds,
		})
	}))
	t.Cleanup(node.Close)
	first, _, store := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	second, _, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	second.playbackStore = store // Distinct handlers/managers share canonical work ownership.
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	start := func(handler *PlaybackHandler) error {
		return handler.startRemoteTranscode(t.Context(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
	}
	go func() { firstDone <- start(first) }()
	select {
	case <-cleaning:
	case <-time.After(3 * time.Second):
		t.Fatal("ambiguous start did not enter owned cleanup")
	}
	go func() { secondDone <- start(second) }()
	waitRecipeCondition(t, "successor waiting for old cleanup", func() bool {
		store.recipeWorkMu.Lock()
		defer store.recipeWorkMu.Unlock()
		return store.recipeWorks["upstream-1"] != nil && store.recipeWorks["upstream-1"].refs == 2
	})
	if starts.Load() != 1 {
		t.Fatalf("successor dispatched before old cleanup: starts=%d", starts.Load())
	}
	releaseAccept()
	releaseCleanup()
	select {
	case err := <-firstDone:
		if err == nil || recipePublicationUncertain(err) {
			t.Fatalf("first start = %v, want definite failure after acknowledged cleanup", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old start did not finish cleanup")
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal("successor failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successor did not complete")
	}
	if starts.Load() != 2 || killed.Load() != 1 || !alive.Load() {
		t.Fatalf("starts=%d killed=%d alive=%v, want 2/1/true", starts.Load(), killed.Load(), alive.Load())
	}
	stored, ok := store.Get("play-1")
	if !ok || !stored.TranscodeStarted || stored.Recipe == nil || stored.Recipe.TranscodeNodeURL != node.URL {
		t.Fatal("successor's accepted recipe was not preserved")
	}
}
