package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func waitRecipeCondition(t *testing.T, why string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal(why)
		}
		time.Sleep(time.Millisecond)
	}
}
func TestRecipeRemoteOwnershipBoundsDetachedBackendsAndReleasesCancellation(t *testing.T) {
	pool, _, _, _ := recipeDurableFixture(t)
	config := pool.Config().Copy()
	config.MaxConns = 1
	app := "ownership-budget-" + uuid.NewString()
	config.ConnConfig.RuntimeParams["application_name"] = app
	limited, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	state := NewDurableCompatPlaybackStore(limited, time.Hour, nil)
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer func() { release(); limited.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- state.WithRemoteStart(t.Context(), "budget-A", func(ctx context.Context) error {
			// Ordinary callback DB reads must work with MaxConns1 while ownership lives.
			var pooled, owned uint32
			if e := limited.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pooled); e != nil {
				return e
			}
			if e := recipeOwner(ctx, state, "budget-A").locked.conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&owned); e != nil {
				return e
			}
			if pooled == owned {
				return errors.New("callback reused the owned session as pooled capacity")
			}
			close(entered)
			<-resume
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("MaxConns1 callback starved")
	}
	canceled, cancel := context.WithCancel(t.Context())
	defer cancel()
	blocked := make(chan error, 1)
	var ran atomic.Bool
	go func() {
		blocked <- state.WithRemoteStart(canceled, "budget-B", func(context.Context) error { ran.Store(true); return nil })
	}()
	waitRecipeCondition(t, "second work did not wait for backend admission", func() bool {
		state.mem.recipeWorkMu.Lock()
		defer state.mem.recipeWorkMu.Unlock()
		return state.mem.recipeWorks["budget-B"] != nil
	})
	var backends int
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE application_name=$1", app).Scan(&backends); err != nil {
		t.Fatal(err)
	}
	if backends != 2 {
		t.Fatalf("ownership exceeded one admitted plus one pooled backend: %d", backends)
	}
	cancel()
	if err = <-blocked; !errors.Is(err, context.Canceled) || !recipePublicationUncertain(err) || ran.Load() {
		t.Fatal("canceled admission entered callback", err)
	}
	release()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("callback rejected")
	if err = state.WithRemoteStart(t.Context(), "budget-C", func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(state.mem.recipeAdmission) != 0 {
		t.Fatal("ownership callback leaked backend admission")
	}
	state.mem.recipeWorkMu.Lock()
	remaining := len(state.mem.recipeWorks)
	state.mem.recipeWorkMu.Unlock()
	if remaining != 0 {
		t.Fatal("ownership leaked local work gates")
	}
	waitRecipeCondition(t, "detached owner returned to or leaked outside the pool", func() bool {
		var n int
		e := pool.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE application_name=$1", app).Scan(&n)
		return e == nil && n <= 1
	})
}
func TestRecipeRemoteOwnershipMemorySerializesAliasHandlersAndAllowsGet(t *testing.T) {
	_, _, recipes, _ := recipeDurableFixture(t)
	node, starts, deletes := recipeCallerNode(t)
	state := NewPlaybackSessionStore(time.Hour, nil)
	work := uuid.NewString()
	alias := work + "-alias"
	state.Put(PlaybackSession{ID: work, UpstreamSessionID: work})
	state.Put(PlaybackSession{ID: alias, UpstreamSessionID: work})
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	firstRecipes := &failedRecipePublicationStore{Store: recipes, failure: errors.New("first publication rejected"), after: func(ctx context.Context, _ string, _ playback.RecipeCard) error {
		close(entered)
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	a, am, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	a.playbackStore = state
	a.RecipeNodeStore = firstRecipes
	b, bm, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	b.playbackStore = state
	b.RecipeNodeStore = recipes
	for _, manager := range []*testCompatSessionManager{am, bm} {
		manager.sessions[work] = manager.sessions["upstream-1"]
		manager.sessions[work].ID = work
		delete(manager.sessions, "upstream-1")
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		first <- a.startRemoteTranscode(t.Context(), work, work, testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/approved/rejected"}, 0, node.URL)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("memory start deadlocked Get/Update")
	}
	go func() {
		second <- b.startRemoteTranscode(t.Context(), alias, work, testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/approved/successor"}, 0, node.URL)
	}()
	waitRecipeCondition(t, "alias handler did not wait on canonical work", func() bool {
		state.recipeWorkMu.Lock()
		defer state.recipeWorkMu.Unlock()
		return state.recipeWorks[work] != nil && state.recipeWorks[work].refs == 2
	})
	if starts.Load() != 1 {
		t.Fatal("memory successor started before rejected cleanup")
	}
	release()
	if <-first == nil {
		t.Fatal("rejected memory publication succeeded")
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 2 || deletes.Load() != 1 {
		t.Fatal("shared memory ownership did not sequence cleanup before successor")
	}
}
func TestRecipeRemoteOwnershipFailedPostReleasesForSuccessor(t *testing.T) {
	_, _, recipes, state := recipeDurableFixture(t)
	var posts, deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if posts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var request transcodenode.TranscodeStartRequest
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			t.Error(e)
			return
		}
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", AudioRecipeVersion: request.AudioRecipeVersion, CopyFMP4RecipeVersion: request.CopyFMP4RecipeVersion, ThrottleSeconds: request.ThrottleSeconds})
	}))
	defer node.Close()
	state.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	for attempt := 0; attempt < 2; attempt++ {
		h, _, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
		h.playbackStore = state
		h.RecipeNodeStore = recipes
		err := h.startRemoteTranscode(t.Context(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/approved/successor"}, 0, node.URL)
		if attempt == 0 && err == nil || attempt == 1 && err != nil {
			t.Fatal("failed POST did not release work for successor", err)
		}
	}
	if posts.Load() != 2 || deletes.Load() != 0 {
		t.Fatal("failed POST scheduled cleanup after ownership release")
	}
}
func TestRecipeRemoteOwnershipLostAfterPostDoesNotRepublishOrDeleteSuccessor(t *testing.T) {
	pool, _, recipes, state := recipeDurableFixture(t)
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	var posts, deletes, alive atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			alive.Store(0)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		n := posts.Add(1)
		alive.Store(n)
		if n == 1 {
			close(entered)
			<-resume
		}
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: req.SessionID, Status: "started", AudioRecipeVersion: req.AudioRecipeVersion, CopyFMP4RecipeVersion: req.CopyFMP4RecipeVersion, ThrottleSeconds: req.ThrottleSeconds})
	}))
	defer func() { release(); node.Close() }()
	work := uuid.NewString()
	state.Put(PlaybackSession{ID: work, UpstreamSessionID: work})
	config := pool.Config().Copy()
	config.MaxConns = 1
	app := "lost-owner-" + uuid.NewString()
	config.ConnConfig.RuntimeParams["application_name"] = app
	affected, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); affected.Close() }()
	a, am, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	a.playbackStore = NewDurableCompatPlaybackStore(affected, time.Hour, nil)
	a.RecipeNodeStore = recipes
	b, bm, _ := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	b.playbackStore = state
	b.RecipeNodeStore = recipes
	for _, manager := range []*testCompatSessionManager{am, bm} {
		manager.sessions[work] = manager.sessions["upstream-1"]
		manager.sessions[work].ID = work
		delete(manager.sessions, "upstream-1")
	}
	first := make(chan error, 1)
	go func() {
		first <- a.startRemoteTranscode(t.Context(), work, work, testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/approved/stale-owner"}, 0, node.URL)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first POST not observed")
	}
	var terminated bool
	if err = pool.QueryRow(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity a WHERE application_name=$1 AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.granted)`, app).Scan(&terminated); err != nil || !terminated {
		t.Fatal("could not terminate actual ownership backend", err)
	}
	if err = b.startRemoteTranscode(t.Context(), work, work, testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/approved/accepted-successor"}, 0, node.URL); err != nil {
		t.Fatal(err)
	}
	release()
	if err = <-first; !recipePublicationUncertain(err) {
		t.Fatal("lost external-start ownership was renewed", err)
	}
	row := readRecipeRow(t, pool, work)
	card, found := recipes.Get(t.Context(), work)
	if row.Recipe == nil || row.Recipe.InputPath != "/approved/accepted-successor" || !found || card.InputPath != row.Recipe.InputPath || deletes.Load() != 0 || alive.Load() != 2 {
		t.Fatal("lost owner republished or deleted accepted successor")
	}
}

func TestRecipeRemoteOwnershipContextCannotRenewExpiredOrDifferentWork(t *testing.T) {
	state := NewPlaybackSessionStore(time.Hour, nil)
	var owned context.Context
	if err := state.WithRemoteStart(t.Context(), "scope-A", func(ctx context.Context) error {
		owned = ctx
		if err := state.WithRemoteStart(ctx, "scope-B", func(context.Context) error { t.Error("different work inherited ownership"); return nil }); !recipePublicationUncertain(err) {
			t.Fatal("different-work context renewed ownership", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.WithRemoteStart(owned, "scope-A", func(context.Context) error { t.Error("released context renewed ownership"); return nil }); !recipePublicationUncertain(err) {
		t.Fatal("released ownership was reused", err)
	}
}
