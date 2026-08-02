package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/contracts/complexv22"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPlaybackControlRequestEmbedsSharedTerminateRequest(t *testing.T) {
	typ := reflect.TypeOf(playbackControlRequest{})
	field, ok := typ.FieldByName("TerminateRequest")
	if !ok || !field.Anonymous || field.Type != reflect.TypeOf(complexv22.TerminateRequest{}) {
		t.Fatalf("playback control request must anonymously embed shared TerminateRequest; field=%+v ok=%v", field, ok)
	}

	var request playbackControlRequest
	if err := json.Unmarshal([]byte(`{"reason":"legacy admin reason","session_generation":"g1","snapshot_id":"s1","reason_code":"limit","idempotency_key":"k1"}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.Reason != "legacy admin reason" || request.SessionGeneration != "g1" || request.SnapshotID != "s1" || request.ReasonCode != "limit" || request.IdempotencyKey != "k1" {
		t.Fatalf("decoded request lost shared or legacy fields: %+v", request)
	}
	if !request.sessionGenerationPresent || !request.snapshotIDPresent || !request.idempotencyKeyPresent {
		t.Fatalf("custom guard presence validation was not preserved: %+v", request)
	}
}

type adminPlaybackControlTestConn struct {
	mu       sync.Mutex
	messages []any
	err      error
}

func (c *adminPlaybackControlTestConn) WriteJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, v)
	return c.err
}

func (c *adminPlaybackControlTestConn) snapshotMessages() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.messages...)
}

func TestHandleTerminateSession_GenerationBoundTerminationReturns200AndDispatchesReason(t *testing.T) {
	control, sessionMgr, realtimeHub, session := newAdminPlaybackControlTestHandler(t)
	conn := &adminPlaybackControlTestConn{}
	registration := realtimeHub.Register(session.ID, conn)
	defer realtimeHub.Unregister(registration)
	if err := sessionMgr.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection: %v", err)
	}
	storeTerminationSnapshot(t, control, "snapshot-1", session)

	body := `{"reason":"limit enforced","reason_code":"global_stream_limit","title":"Playback stopped","message":"This server's global stream limit was reached.","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"terminate-1"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playbackControlResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "terminated" {
		t.Fatalf("response status = %q, want terminated", response.Status)
	}
	if _, err := sessionMgr.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("GetSession after termination error = %v, want ErrSessionNotFound", err)
	}
	messages := conn.snapshotMessages()
	if len(messages) != 1 {
		t.Fatalf("realtime messages = %d, want 1", len(messages))
	}
	command, ok := messages[0].(playback.CommandEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want CommandEnvelope", messages[0])
	}
	if command.ReasonCode != "global_stream_limit" {
		t.Fatalf("reason_code = %q, want global_stream_limit", command.ReasonCode)
	}
	var display map[string]string
	if err := json.Unmarshal(command.Payload, &display); err != nil {
		t.Fatalf("decode display payload: %v", err)
	}
	if display["title"] != "Playback stopped" || display["message"] != "This server's global stream limit was reached." {
		t.Fatalf("display payload = %#v", display)
	}
}

func TestHandleTerminateSession_ReplaysIdempotentSuccessWithoutRedispatch(t *testing.T) {
	control, _, realtimeHub, session := newAdminPlaybackControlTestHandler(t)
	conn := &adminPlaybackControlTestConn{}
	registration := realtimeHub.Register(session.ID, conn)
	defer realtimeHub.Unregister(registration)
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"terminate-1"}`

	var first playbackControlResponse
	for i := range 2 {
		req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
		rr := httptest.NewRecorder()
		control.HandleTerminateSession(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, body = %s", i+1, rr.Code, rr.Body.String())
		}
		var got playbackControlResponse
		if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
			t.Fatalf("decode response %d: %v", i+1, err)
		}
		if i == 0 {
			first = got
		} else if got != first {
			t.Fatalf("replay response = %+v, want %+v", got, first)
		}
	}
	if got := len(conn.snapshotMessages()); got != 1 {
		t.Fatalf("realtime dispatches = %d, want 1", got)
	}
}

func TestHandleTerminateSession_ConcurrentIdenticalRequestsAreLinearizable(t *testing.T) {
	control, _, realtimeHub, session := newAdminPlaybackControlTestHandler(t)
	conn := &adminPlaybackControlTestConn{}
	registration := realtimeHub.Register(session.ID, conn)
	defer realtimeHub.Unregister(registration)
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"concurrent"}`

	const workers = 20
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
			rr := httptest.NewRecorder()
			control.HandleTerminateSession(rr, req)
			results <- rr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var commandID string
	for rr := range results {
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playbackControlResponse
		if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if commandID == "" {
			commandID = response.CommandID
		} else if response.CommandID != commandID {
			t.Fatalf("command ID = %q, want replay %q", response.CommandID, commandID)
		}
	}
	if got := len(conn.snapshotMessages()); got != 1 {
		t.Fatalf("realtime dispatches = %d, want 1", got)
	}
}

func TestHandleTerminateSession_EndedAndUnknownAre404(t *testing.T) {
	t.Run("ended", func(t *testing.T) {
		control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
		storeTerminationSnapshot(t, control, "snapshot-ended", session)
		if err := sessionMgr.TerminateSessionGeneration(t.Context(), session.ID, session.Generation, nil); err != nil {
			t.Fatalf("pre-terminate: %v", err)
		}
		body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-ended","idempotency_key":"new-key"}`
		req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
		rr := httptest.NewRecorder()
		control.HandleTerminateSession(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("unknown", func(t *testing.T) {
		control, _, _, _ := newAdminPlaybackControlTestHandler(t)
		generation := "58d591a3-c011-481d-b837-b2c9c8032b9a"
		if err := control.snapshots.Store("snapshot-unknown-session", time.Now().UTC(), true, []playback.SnapshotSessionIdentity{{SessionID: "unknown", Generation: generation}}); err != nil {
			t.Fatalf("Store snapshot: %v", err)
		}
		body := `{"reason_code":"global_stream_limit","session_generation":"` + generation + `","snapshot_id":"snapshot-unknown-session","idempotency_key":"new-key"}`
		req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/unknown/terminate", strings.NewReader(body)), "session_id", "unknown")
		rr := httptest.NewRecorder()
		control.HandleTerminateSession(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
	})
}

func TestHandleTerminateSession_ReplaysAlreadyTerminatedHTTP200(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	if err := sessionMgr.TerminateSessionGeneration(t.Context(), session.ID, session.Generation, nil); err != nil {
		t.Fatalf("pre-terminate: %v", err)
	}
	binding := playback.TerminationBinding{
		ServerID:   "server-1",
		SessionID:  session.ID,
		Generation: session.Generation,
		SnapshotID: "snapshot-1",
		ReasonCode: "global_stream_limit",
		DeadlineMS: 3000,
	}
	if _, _, err := control.idempotency.Do("already", binding, func() (playback.TerminationResult, error) {
		return playback.TerminationResult{Status: playback.TerminationStatusAlreadyTerminated, CommandID: "command-already"}, nil
	}); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"already"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playbackControlResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "already_terminated" || response.CommandID != "command-already" {
		t.Fatalf("response = %+v", response)
	}
}

func TestHandleTerminateSession_RejectsStaleSnapshot(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	now := time.Now().UTC()
	control.snapshots.SetClock(func() time.Time { return now })
	if err := control.snapshots.Store("snapshot-stale", now, true, []playback.SnapshotSessionIdentity{{SessionID: session.ID, Generation: session.Generation}}); err != nil {
		t.Fatalf("Store snapshot: %v", err)
	}
	control.snapshots.SetClock(func() time.Time { return now.Add(SessionSnapshotFreshness) })
	body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-stale","idempotency_key":"stale"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got, err := sessionMgr.GetSession(session.ID); err != nil || got.Generation != session.Generation {
		t.Fatalf("stale snapshot changed session: got=%+v err=%v", got, err)
	}
}

type failingAdminTerminationTombstones struct{ err error }

func (s failingAdminTerminationTombstones) WasSessionGenerationEnded(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

func (s failingAdminTerminationTombstones) RecordEndedSessionGeneration(context.Context, string, string, time.Time) error {
	return s.err
}

type cancellationBoundaryTombstones struct{ cancel context.CancelFunc }

func (s cancellationBoundaryTombstones) WasSessionGenerationEnded(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

func (s cancellationBoundaryTombstones) RecordEndedSessionGeneration(context.Context, string, string, time.Time) error {
	s.cancel()
	return nil
}

type contextCapturingSessionSyncer struct{ result chan error }

func (s contextCapturingSessionSyncer) SyncNow(ctx context.Context) error {
	s.result <- ctx.Err()
	return nil
}

type preLifecycleUpdatingSessionManager struct {
	*playback.SessionManager
	snapshots chan *playback.Session
}

func (m *preLifecycleUpdatingSessionManager) TerminateSessionGenerationSnapshotStatus(ctx context.Context, sessionID, generation string, finalizer func(*playback.Session) error) (playback.TerminationStatus, error) {
	if err := m.UpdateProgressGeneration(sessionID, generation, 456.25, true); err != nil {
		return "", err
	}
	return m.SessionManager.TerminateSessionGenerationSnapshotStatus(ctx, sessionID, generation, func(snapshot *playback.Session) error {
		m.snapshots <- snapshot
		return finalizer(snapshot)
	})
}

func TestHandleTerminateSession_TombstoneFailureLeavesLiveStateAndSendsNothing(t *testing.T) {
	control, sessionMgr, realtimeHub, session := newAdminPlaybackControlTestHandler(t)
	sessionMgr.SetSessionGenerationTombstoneStore(failingAdminTerminationTombstones{err: errors.New("write failed")})
	conn := &adminPlaybackControlTestConn{}
	registration := realtimeHub.Register(session.ID, conn)
	defer realtimeHub.Unregister(registration)
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"failed"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got, err := sessionMgr.GetSession(session.ID); err != nil || got.Generation != session.Generation {
		t.Fatalf("live session changed: got=%+v err=%v", got, err)
	}
	if got := len(conn.snapshotMessages()); got != 0 {
		t.Fatalf("realtime dispatches = %d, want 0", got)
	}
}

func TestHandleTerminateSession_PostTombstoneCleanupOwnsBoundedServerContext(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	requestCtx, cancel := context.WithCancel(context.Background())
	sessionMgr.SetSessionGenerationTombstoneStore(cancellationBoundaryTombstones{cancel: cancel})
	syncResult := make(chan error, 1)
	control.playback.SessionSyncer = contextCapturingSessionSyncer{result: syncResult}
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"cancel-boundary"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)).WithContext(requestCtx), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if err := <-syncResult; err != nil {
		t.Fatalf("post-tombstone sync context error = %v, want server-owned live context", err)
	}
}

func TestHandleTerminateSession_FinalizerUsesManagerSnapshotAfterHandlerLookup(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	snapshots := make(chan *playback.Session, 1)
	control.playback.sessionMgr = &preLifecycleUpdatingSessionManager{SessionManager: sessionMgr, snapshots: snapshots}
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	body := `{"session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"fresh-snapshot"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	snapshot := <-snapshots
	if snapshot == nil || snapshot.Position != 456.25 || !snapshot.IsPaused {
		t.Fatalf("finalizer snapshot = %+v, want post-lookup state", snapshot)
	}
}

func TestHandleTerminateSession_RejectsOversizedBodyAndFields(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
		body := `{"reason":"` + strings.Repeat("x", 20<<10) + `"}`
		req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
		rr := httptest.NewRecorder()
		control.HandleTerminateSession(rr, req)
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		if _, err := sessionMgr.GetSession(session.ID); err != nil {
			t.Fatalf("oversized body changed session: %v", err)
		}
	})
	for _, test := range []struct {
		name string
		key  string
		msg  string
	}{
		{name: "idempotency key", key: strings.Repeat("k", 256)},
		{name: "message", key: "key", msg: strings.Repeat("m", 4097)},
	} {
		t.Run(test.name, func(t *testing.T) {
			control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
			storeTerminationSnapshot(t, control, "snapshot-1", session)
			body := `{"message":"` + test.msg + `","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"` + test.key + `"}`
			req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
			rr := httptest.NewRecorder()
			control.HandleTerminateSession(rr, req)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if _, err := sessionMgr.GetSession(session.ID); err != nil {
				t.Fatalf("oversized field changed session: %v", err)
			}
		})
	}
}

func TestHandleTerminateSession_RealtimeWriteFailureFallsBackToCompletedTermination(t *testing.T) {
	control, sessionMgr, realtimeHub, session := newAdminPlaybackControlTestHandler(t)
	conn := &adminPlaybackControlTestConn{err: errors.New("socket closed")}
	registration := realtimeHub.Register(session.ID, conn)
	defer realtimeHub.Unregister(registration)
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"write-failed"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := sessionMgr.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("GetSession error = %v, want completed fallback termination", err)
	}
}

func TestHandleTerminateSession_LegacyTombstoneFailureReturnsErrorAndKeepsState(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	sessionMgr.SetSessionGenerationTombstoneStore(failingAdminTerminationTombstones{err: errors.New("write failed")})
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(`{"deadline_ms":10}`)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	time.Sleep(40 * time.Millisecond)
	if got, err := sessionMgr.GetSession(session.ID); err != nil || got.Generation != session.Generation {
		t.Fatalf("legacy fallback changed live session: got=%+v err=%v", got, err)
	}
}

func TestHandleTerminateSession_LegacyRealtimeTombstoneFailureSendsNothing(t *testing.T) {
	control, sessionMgr, realtimeHub, session := newAdminPlaybackControlTestHandler(t)
	sessionMgr.SetSessionGenerationTombstoneStore(failingAdminTerminationTombstones{err: errors.New("write failed")})
	conn := &adminPlaybackControlTestConn{}
	registration := realtimeHub.Register(session.ID, conn)
	defer realtimeHub.Unregister(registration)
	if err := sessionMgr.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection: %v", err)
	}
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(`{"deadline_ms":10}`)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := len(conn.snapshotMessages()); got != 0 {
		t.Fatalf("realtime messages = %d, want 0", got)
	}
	if got, err := sessionMgr.GetSession(session.ID); err != nil || got.Generation != session.Generation {
		t.Fatalf("legacy failure changed session: got=%+v err=%v", got, err)
	}
}

func TestHandleTerminateSession_RejectsUnsafeGenerationBoundRequests(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "one supplied empty safety field", body: `{"session_generation":""}`, status: http.StatusUnprocessableEntity},
		{name: "all supplied empty safety fields", body: `{"session_generation":" ","snapshot_id":"","idempotency_key":""}`, status: http.StatusUnprocessableEntity},
		{name: "partial safety fields", body: `{"session_generation":"` + session.Generation + `"}`, status: http.StatusUnprocessableEntity},
		{name: "unknown snapshot", body: `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"unknown","idempotency_key":"key"}`, status: http.StatusConflict},
		{name: "mismatched generation", body: `{"reason_code":"global_stream_limit","session_generation":"7d556533-6ed8-4593-a31e-52c34f0a5cf4","snapshot_id":"snapshot-1","idempotency_key":"key"}`, status: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(test.body)), "session_id", session.ID)
			rr := httptest.NewRecorder()
			control.HandleTerminateSession(rr, req)
			if rr.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, test.status, rr.Body.String())
			}
			if got, err := sessionMgr.GetSession(session.ID); err != nil || got.Generation != session.Generation {
				t.Fatalf("request changed live session: got=%+v err=%v", got, err)
			}
		})
	}
}

func TestHandleTerminateSession_IdempotencyCapacityReturns503(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)
	control.idempotency = playback.NewIdempotencyStore(1, 24*time.Hour)
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = control.idempotency.Do("occupied", playback.TerminationBinding{ServerID: "server-1", SessionID: "other", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a"}, func() (playback.TerminationResult, error) {
			close(started)
			<-release
			return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
		})
	}()
	<-started
	defer func() { close(release); <-done }()
	body := `{"reason_code":"global_stream_limit","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"new"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got, err := sessionMgr.GetSession(session.ID); err != nil || got.Generation != session.Generation {
		t.Fatalf("capacity failure changed session: got=%+v err=%v", got, err)
	}
}

func TestHandleTerminateSession_RejectsConflictingIdempotencyReuse(t *testing.T) {
	control, _, _, session := newAdminPlaybackControlTestHandler(t)
	storeTerminationSnapshot(t, control, "snapshot-1", session)
	binding := playback.TerminationBinding{ServerID: "server-1", SessionID: session.ID, Generation: session.Generation, Reason: "global_stream_limit"}
	if _, _, err := control.idempotency.Do("shared", binding, func() (playback.TerminationResult, error) {
		return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
	}); err != nil {
		t.Fatalf("seed idempotency: %v", err)
	}
	body := `{"reason_code":"manual_override","session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"shared"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleTerminateSession_IdempotencyBindsSnapshotPayloadAndDeadline(t *testing.T) {
	tests := []struct {
		name       string
		secondBody func(generation string) string
	}{
		{"snapshot", func(generation string) string {
			return `{"reason_code":"global_stream_limit","title":"Stopped","message":"Limit","deadline_ms":3001,"session_generation":"` + generation + `","snapshot_id":"snapshot-2","idempotency_key":"shared"}`
		}},
		{"display payload", func(generation string) string {
			return `{"reason_code":"global_stream_limit","title":"Other","message":"Different","deadline_ms":3001,"session_generation":"` + generation + `","snapshot_id":"snapshot-1","idempotency_key":"shared"}`
		}},
		{"normalized deadline", func(generation string) string {
			return `{"reason_code":"global_stream_limit","title":"Stopped","message":"Limit","deadline_ms":3002,"session_generation":"` + generation + `","snapshot_id":"snapshot-1","idempotency_key":"shared"}`
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control, _, _, session := newAdminPlaybackControlTestHandler(t)
			storeTerminationSnapshot(t, control, "snapshot-1", session)
			storeTerminationSnapshot(t, control, "snapshot-2", session)
			firstBody := `{"reason_code":"global_stream_limit","title":"Stopped","message":"Limit","deadline_ms":3001,"session_generation":"` + session.Generation + `","snapshot_id":"snapshot-1","idempotency_key":"shared"}`
			for index, body := range []string{firstBody, test.secondBody(session.Generation)} {
				req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/terminate", strings.NewReader(body)), "session_id", session.ID)
				rr := httptest.NewRecorder()
				control.HandleTerminateSession(rr, req)
				want := http.StatusOK
				if index == 1 {
					want = http.StatusConflict
				}
				if rr.Code != want {
					t.Fatalf("request %d status = %d, want %d; body=%s", index+1, rr.Code, want, rr.Body.String())
				}
			}
		})
	}
}

func TestHandleTerminateSession_RejectsReusedSessionIDGeneration(t *testing.T) {
	control, sessionMgr, _, first := newAdminPlaybackControlTestHandler(t)
	storeTerminationSnapshot(t, control, "snapshot-g1", first)
	if err := sessionMgr.TerminateSessionGeneration(context.Background(), first.ID, first.Generation, nil); err != nil {
		t.Fatalf("terminate first generation: %v", err)
	}
	second := *first
	second.Generation = "7d556533-6ed8-4593-a31e-52c34f0a5cf4"
	if _, err := sessionMgr.RegisterReconstructedChecked(context.Background(), &second); err != nil {
		t.Fatalf("register reused ID: %v", err)
	}
	body := `{"reason_code":"global_stream_limit","session_generation":"` + first.Generation + `","snapshot_id":"snapshot-g1","idempotency_key":"stale-g1"}`
	req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/admin/sessions/"+first.ID+"/terminate", strings.NewReader(body)), "session_id", first.ID)
	rr := httptest.NewRecorder()
	control.HandleTerminateSession(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	got, err := sessionMgr.GetSession(first.ID)
	if err != nil || got.Generation != second.Generation {
		t.Fatalf("new generation was disturbed: got=%+v err=%v", got, err)
	}
}

func TestHandlePauseSession_RequiresHelloReadyControlConnection(t *testing.T) {
	control, sessionMgr, realtimeHub, session := newAdminPlaybackControlTestHandler(t)

	registration := realtimeHub.Register(session.ID, &adminPlaybackControlTestConn{})
	if registration == nil {
		t.Fatal("expected raw realtime registration")
	}
	defer realtimeHub.Unregister(registration)

	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/pause", strings.NewReader(`{"deadline_ms":10}`))
	req = withPlaybackRouteParam(req, "session_id", session.ID)

	rr := httptest.NewRecorder()
	control.HandlePauseSession(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error != "realtime_unavailable" {
		t.Fatalf("error code = %q, want realtime_unavailable", resp.Error)
	}

	time.Sleep(40 * time.Millisecond)

	if _, err := sessionMgr.GetSession(session.ID); err != nil {
		t.Fatalf("pause should not schedule a fallback stop, GetSession: %v", err)
	}
}

func TestHandleStopSession_KeepsFallbackWhenRealtimeUnavailable(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/stop", strings.NewReader(`{"deadline_ms":10}`))
	req = withPlaybackRouteParam(req, "session_id", session.ID)

	rr := httptest.NewRecorder()
	control.HandleStopSession(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackControlResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "fallback_scheduled" {
		t.Fatalf("status = %q, want fallback_scheduled", resp.Status)
	}

	waitForPlaybackSessionMissing(t, sessionMgr, session.ID)
}

func TestPlaybackSessionRowJSONIncludesHasPlaybackControl(t *testing.T) {
	row := playbackSessionRow{
		SessionID:          "session-1",
		HasPlaybackControl: true,
	}

	data, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	value, ok := decoded["has_playback_control"].(bool)
	if !ok {
		t.Fatalf("has_playback_control missing from JSON: %s", string(data))
	}
	if !value {
		t.Fatalf("has_playback_control = %v, want true", value)
	}
}

func newAdminPlaybackControlTestHandler(t *testing.T) (*AdminPlaybackControlHandler, *playback.SessionManager, *playback.RealtimeHub, *playback.Session) {
	t.Helper()

	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	realtimeHub := playback.NewRealtimeHub()
	commandTracker := playback.NewCommandTracker()
	t.Cleanup(commandTracker.Close)

	playbackHandler := NewPlaybackHandler(sessionMgr)
	playbackHandler.RealtimeHub = realtimeHub
	playbackHandler.CommandTracker = commandTracker
	playbackHandler.CommandDispatcher = playback.NewCommandDispatcher(sessionMgr, realtimeHub, commandTracker)

	registry := playback.NewSnapshotRegistry(16, 45*time.Second)
	idempotency := playback.NewIdempotencyStore(16, 24*time.Hour)
	return NewGuardedAdminPlaybackControlHandler(playbackHandler, registry, idempotency, "server-1"), sessionMgr, realtimeHub, session
}

func storeTerminationSnapshot(t *testing.T, control *AdminPlaybackControlHandler, snapshotID string, session *playback.Session) {
	t.Helper()
	if err := control.snapshots.Store(snapshotID, time.Now().UTC(), true, []playback.SnapshotSessionIdentity{{SessionID: session.ID, Generation: session.Generation}}); err != nil {
		t.Fatalf("Store snapshot: %v", err)
	}
}

func waitForPlaybackSessionMissing(t *testing.T, sessionMgr *playback.SessionManager, sessionID string) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, err := sessionMgr.GetSession(sessionID)
		if errors.Is(err, playback.ErrSessionNotFound) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	session, err := sessionMgr.GetSession(sessionID)
	if err == nil && session != nil {
		t.Fatalf("session %q still exists after fallback deadline", sessionID)
	}
	t.Fatalf("unexpected GetSession result after fallback deadline: %v", err)
}
