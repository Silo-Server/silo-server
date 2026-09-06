package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSharedPingPreservesOwnerAndArbitratesExpiry(t *testing.T) {
	pool := newCompatTestPool(t)
	ownerStore := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	peerStore := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	owner := playback.NewSessionManager(1, 1)
	peer := playback.NewSessionManager(0, 0)
	owner.SetCompatActivityReader(ownerStore.NativeSessionActivity)
	owner.SetCompatExpiryClaimer(ownerStore.ClaimNativeSessionExpiry)
	native, err := owner.StartSession(42, "profile-ping", 1, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	native.IsJellyfinCompat = true
	native.IsPaused = true
	native.Position = 321
	native.LastActivityAt = time.Now().Add(-time.Hour)
	native.UpdatedAt = native.LastActivityAt
	id := "ping-" + native.ID
	token := "token-" + native.ID
	ownerStore.Put(PlaybackSession{ID: id, CompatToken: token, UserID: PseudoUserID(42, "profile-ping").String(), UpstreamSessionID: native.ID})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM jellycompat_playback_sessions WHERE id=$1", id)
	})
	auth := &Session{Token: token, StreamAppUserID: 42, ProfileID: "profile-ping"}
	handler := &PlaybackHandler{playbackStore: peerStore, sessionMgr: peer}
	ping := func() int {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.HandleSessionPlayingPing(rec, viewerRequest(http.MethodPost, "/?playSessionId="+id, "", "", "", auth))
		return rec.Code
	}
	if status := ping(); status != 204 {
		t.Fatalf("peer ping=%d", status)
	}
	if _, err := owner.StartSession(42, "profile-ping", 2, playback.PlayDirect, false); !errors.Is(err, playback.ErrTooManyStreams) {
		t.Fatalf("stale owner admitted beyond the live shared session limit: %v", err)
	}
	if expired := owner.CleanInactive(time.Minute, time.Minute); len(expired) != 0 {
		t.Fatal("owner expired successfully pinged paused session")
	}
	got, err := owner.GetSession(native.ID)
	if err != nil || got.Position != 321 || !got.IsPaused {
		t.Fatalf("state=%+v err=%v", got, err)
	}
	// Force a stale read, then commit a ping before the owner's final claim.
	old := time.Now().Add(-time.Hour)
	native.LastActivityAt = old
	native.UpdatedAt = old
	if _, err := pool.Exec(t.Context(), `UPDATE jellycompat_playback_sessions SET data=jsonb_set(data,'{UpdatedAt}',to_jsonb($2::timestamptz)) WHERE id=$1`, id, old); err != nil {
		t.Fatal(err)
	}
	owner.SetCompatActivityReader(func(ctx context.Context, s []playback.Session) (map[string]time.Time, error) {
		activity, err := ownerStore.NativeSessionActivity(ctx, s)
		if status := ping(); status != 204 {
			t.Fatalf("racing ping=%d", status)
		}
		return activity, err
	})
	if expired := owner.CleanInactive(time.Minute, time.Minute); len(expired) != 0 {
		t.Fatal("stale owner read defeated successful concurrent ping")
	}
	// If retirement wins first, a subsequent ping must fail rather than
	// acknowledge activity for the now-closed native playback session.
	owner.SetCompatActivityReader(ownerStore.NativeSessionActivity)
	if _, err := pool.Exec(t.Context(), `UPDATE jellycompat_playback_sessions SET data=jsonb_set(data,'{UpdatedAt}',to_jsonb($2::timestamptz)) WHERE id=$1`, id, old); err != nil {
		t.Fatal(err)
	}
	var expiredHooks int
	owner.AddExpirationHook(func(*playback.Session) { expiredHooks++ })
	if expired := owner.CleanInactive(time.Minute, time.Minute); len(expired) != 1 || expiredHooks != 1 {
		t.Fatalf("expired=%d hooks=%d", len(expired), expiredHooks)
	}
	if status := ping(); status != 404 {
		t.Fatalf("post-retirement ping=%d", status)
	}
	if _, err := owner.StartSession(42, "profile-ping", 2, playback.PlayDirect, false); err != nil {
		t.Fatalf("durable retirement did not release admission slot: %v", err)
	}
	if _, ok := peerStore.GetFinalizable(id, token); !ok {
		t.Fatal("retirement lost final Stopped report mapping")
	}
}

func TestSharedActivityOwnershipAndFailure(t *testing.T) {
	pool := newCompatTestPool(t)
	store := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	native := playback.Session{ID: fmt.Sprintf("activity-%d", time.Now().UnixNano()), UserID: 41, ProfileID: "parent", IsJellyfinCompat: true}
	id := "compat-" + native.ID
	store.Put(PlaybackSession{ID: id, CompatToken: "activity-token", UserID: PseudoUserID(41, "child").String(), UpstreamSessionID: native.ID})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM jellycompat_playback_sessions WHERE id=$1", id)
	})
	activity, err := store.NativeSessionActivity(t.Context(), []playback.Session{native})
	if err != nil || len(activity) != 0 {
		t.Fatalf("foreign profile activity=%v err=%v", activity, err)
	}
	manager := playback.NewSessionManager(0, 0)
	compat, err := manager.StartSession(41, "parent", 1, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	compat.IsJellyfinCompat = true
	compat.LastActivityAt = time.Now().Add(-time.Hour)
	ordinary, err := manager.StartSession(41, "parent", 2, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	ordinary.LastActivityAt = time.Now().Add(-time.Hour)
	manager.SetCompatActivityReader(func(context.Context, []playback.Session) (map[string]time.Time, error) {
		return nil, errors.New("store unavailable")
	})
	manager.SetCompatExpiryClaimer(store.ClaimNativeSessionExpiry)
	expired := manager.CleanInactive(time.Minute, time.Minute)
	if len(expired) != 1 || expired[0].ID != ordinary.ID {
		t.Fatalf("failure must protect only compat, expired=%+v", expired)
	}
}

func TestSharedExpiryClaimTimeoutPreservesSession(t *testing.T) {
	pool := newCompatTestPool(t)
	store := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	manager := playback.NewSessionManager(0, 0)
	native, err := manager.StartSession(42, "timeout", 1, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	native.IsJellyfinCompat = true
	old := time.Now().Add(-time.Hour)
	native.LastActivityAt = old
	id := "claim-timeout-" + native.ID
	token := "claim-token-" + native.ID
	store.Put(PlaybackSession{ID: id, CompatToken: token, UserID: PseudoUserID(42, "timeout").String(), UpstreamSessionID: native.ID})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM jellycompat_playback_sessions WHERE id=$1", id)
	})
	if _, err := pool.Exec(t.Context(), `UPDATE jellycompat_playback_sessions SET data=jsonb_set(data,'{UpdatedAt}',to_jsonb($2::timestamptz)) WHERE id=$1`, id, old); err != nil {
		t.Fatal(err)
	}
	manager.SetCompatActivityReader(store.NativeSessionActivity)
	manager.SetCompatExpiryClaimer(store.ClaimNativeSessionExpiry)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `SELECT id FROM jellycompat_playback_sessions WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != 0 {
		t.Fatal("timed-out claim removed native session")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A client timeout cannot prove whether the database committed before its
	// cancellation arrived. Either outcome must converge without losing a
	// successful ping: a committed retirement rejects it, a canceled one accepts it.
	pingErr := store.TouchActiveForToken(t.Context(), id, token)
	if pingErr != nil && !errors.Is(pingErr, ErrSessionNotFound) {
		t.Fatal(pingErr)
	}
	wantExpired := 0
	if errors.Is(pingErr, ErrSessionNotFound) {
		wantExpired = 1
	}
	if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != wantExpired {
		t.Fatalf("claim timeout did not converge: ping=%v expired=%d want=%d", pingErr, len(expired), wantExpired)
	}
}

func TestDurablePingDoesNotMoveActivityBackwards(t *testing.T) {
	pool := newCompatTestPool(t)
	store := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	id := fmt.Sprintf("monotonic-ping-%d", time.Now().UnixNano())
	store.Put(PlaybackSession{ID: id, CompatToken: id})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM jellycompat_playback_sessions WHERE id=$1", id)
	})
	future := time.Now().Add(time.Minute).Truncate(time.Microsecond)
	if _, err := pool.Exec(t.Context(), `UPDATE jellycompat_playback_sessions SET data=jsonb_set(data,'{UpdatedAt}',to_jsonb($2::timestamptz)) WHERE id=$1`, id, future); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchActiveForToken(t.Context(), id, id); err != nil {
		t.Fatal(err)
	}
	var got time.Time
	if err := pool.QueryRow(t.Context(), `SELECT (data->>'UpdatedAt')::timestamptz FROM jellycompat_playback_sessions WHERE id=$1`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(future) {
		t.Fatalf("activity moved backwards: got=%s want=%s", got, future)
	}
}
