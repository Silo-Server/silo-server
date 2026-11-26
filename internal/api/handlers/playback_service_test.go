package handlers

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	serviceInstallation      = "0f0e5c2e-2c6b-4b39-9c8b-3a5f9a2b7d11"
	serviceOtherInstallation = "6c0a7b1e-8d2f-4a35-b0c1-2e4d5f6a7b8c"
)

// playbackServiceFixture is a handler with the memory plan store, one live
// session and its attempt row, as the v2 adapter sees them.
type playbackServiceFixture struct {
	handler *PlaybackHandler
	manager *playback.SessionManager
	session *playback.Session
	caller  PlaybackCaller
	ctx     context.Context
}

func newPlaybackServiceFixture(t *testing.T) *playbackServiceFixture {
	t.Helper()
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := newKeyedPlaybackHandler(manager)
	handler.InstallationID = serviceInstallation
	record := playback.AttemptRecordV3{
		PlaybackAttemptID:    uuid.NewString(),
		SessionID:            session.ID,
		UserID:               1,
		ProfileID:            "profile-1",
		RequestedMediaFileID: 100,
		EffectiveMediaFileID: 100,
		ExpiresAt:            time.Now().Add(time.Hour),
	}
	if err := handler.PlanStoreV3.SaveAttempt(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return &playbackServiceFixture{
		handler: handler,
		manager: manager,
		session: session,
		caller:  PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation},
		ctx:     newAuthorizedPlaybackContext(),
	}
}

// stopAttemptAs ends a session's attempt row the way a server-side end does.
func stopAttemptAs(h *PlaybackHandler, sessionID, stopID string) {
	ctx := context.Background()
	claimNativeEnd(ctx, h.PlanStoreV3, h.StreamDeny, sessionID, stopID).finish(ctx, sessionID, "")
}

func assertPlaybackOperationError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var op *PlaybackOperationError
	if !errors.As(err, &op) {
		t.Fatalf("err = %v, want *PlaybackOperationError %d %s", err, status, code)
	}
	if op.Status != status || op.Code != code {
		t.Fatalf("err = %d %s, want %d %s", op.Status, op.Code, status, code)
	}
}

func TestPlaybackCapabilitiesV2IsAlwaysAvailable(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	view, err := f.handler.PlaybackCapabilities(f.ctx, 1, "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "available" || !view.Allowed || view.InstallationID != serviceInstallation || view.Revision == "" {
		t.Fatalf("view = %+v", view)
	}
	if len(view.ProtocolVersions) != 1 || view.ProtocolVersions[0] != playback.ProtocolV3 {
		t.Fatalf("protocol versions = %v", view.ProtocolVersions)
	}
	if !slices.Contains(view.Features, "fixed_media_file_v1") {
		t.Fatal("fixed media file requests are not advertised")
	}
	if !slices.Contains(view.Features, "marker_segments_v1") {
		t.Fatal("marker collections are not advertised")
	}
	if !slices.Contains(view.Features, "bandwidth_probe_v1") {
		t.Fatal("the bandwidth probe is not advertised")
	}
	for _, feature := range []string{"watch_party_source_fallback_v1", "watch_party_coordinator_v1"} {
		if slices.Contains(view.Features, feature) {
			t.Fatalf("unconfigured Watch Party feature advertised: %s", feature)
		}
	}
	if _, err := f.handler.PlaybackCapabilities(f.ctx, 2, "profile-1"); err == nil {
		t.Fatal("foreign identity accepted")
	}
	f.handler.InstallationID = ""
	if _, err := f.handler.PlaybackCapabilities(f.ctx, 1, "profile-1"); err == nil {
		t.Fatal("unconfigured installation reported available")
	}
}

func TestPlaybackCapabilitiesV2AdvertisesConfiguredWatchTogether(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	unconfigured, err := f.handler.PlaybackCapabilities(f.ctx, 1, "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	f.handler.WatchTogetherAvailable = true
	configured, err := f.handler.PlaybackCapabilities(f.ctx, 1, "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"watch_party_source_fallback_v1", "watch_party_coordinator_v1"} {
		if !slices.Contains(configured.Features, feature) {
			t.Fatalf("configured Watch Party feature missing: %s", feature)
		}
	}
	if !slices.Contains(configured.Features, "marker_segments_v1") {
		t.Fatal("marker collections are not advertised with Watch Party configured")
	}
	if configured.Revision == unconfigured.Revision {
		t.Fatal("capability revision did not change with Watch Party availability")
	}
}

func TestPlaybackMutationsV2RefuseOtherInstallation(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	other := f.caller
	other.InstallationID = serviceOtherInstallation
	_, err := f.handler.ApplyProgressV2(f.ctx, other, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10})
	assertPlaybackOperationError(t, err, http.StatusConflict, "installation_changed")
	_, err = f.handler.StopPlaybackV2(f.ctx, other, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
	assertPlaybackOperationError(t, err, http.StatusConflict, "installation_changed")
	err = f.handler.ReportRouteEventV2(f.ctx, other, PlaybackRouteEventCommand{EventID: uuid.NewString()})
	assertPlaybackOperationError(t, err, http.StatusConflict, "installation_changed")
	// The caller must also be the authenticated identity.
	foreign := f.caller
	foreign.UserID = 2
	_, err = f.handler.ApplyProgressV2(f.ctx, foreign, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10})
	assertPlaybackOperationError(t, err, http.StatusForbidden, "forbidden")
}

func TestApplyProgressV2SequencesSamplesDurably(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	view, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 2, Position: 20})
	if err != nil {
		t.Fatal(err)
	}
	if view.Outcome != PlaybackOutcomeApplied || view.Accepted == nil || view.Accepted.Sequence != 2 || view.Accepted.Position != 20 {
		t.Fatalf("applied view = %+v", view)
	}
	if live, _ := f.manager.GetSession(f.session.ID); live.Position != 20 {
		t.Fatalf("live position = %v, want 20", live.Position)
	}
	// A lower sequence is stale, even with a higher position, and reports the
	// latest accepted sample.
	view, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 99})
	if err != nil || view.Outcome != PlaybackOutcomeStaleSample || view.Accepted == nil || view.Accepted.Sequence != 2 {
		t.Fatalf("stale view = %+v, %v", view, err)
	}
	if live, _ := f.manager.GetSession(f.session.ID); live.Position != 20 {
		t.Fatalf("stale sample moved the live position to %v", live.Position)
	}
	// Same sequence, same payload replays; different payload conflicts.
	view, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 2, Position: 20})
	if err != nil || view.Outcome != PlaybackOutcomeReplayed {
		t.Fatalf("replay view = %+v, %v", view, err)
	}
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 2, Position: 21})
	assertPlaybackOperationError(t, err, http.StatusConflict, "progress_conflict")
	// A higher sequence wins even when the position moves backward.
	view, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 3, Position: 5, IsPaused: true})
	if err != nil || view.Outcome != PlaybackOutcomeApplied || view.Accepted.Position != 5 || !view.Accepted.IsPaused {
		t.Fatalf("backward view = %+v, %v", view, err)
	}
	// Progress does not need the in-memory session: it is sequenced on the row.
	if err := f.manager.StopSession(f.session.ID); err != nil {
		t.Fatal(err)
	}
	view, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 4, Position: 30})
	if err != nil || view.Outcome != PlaybackOutcomeApplied {
		t.Fatalf("row-only view = %+v, %v", view, err)
	}
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 0, Position: 30})
	assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, uuid.NewString(), PlaybackProgressCommand{Sequence: 1, Position: 30})
	assertPlaybackOperationError(t, err, http.StatusNotFound, "session_not_found")
}

func TestStopPlaybackV2FirstStopWinsAndLaterStopsReplay(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10}); err != nil {
		t.Fatal(err)
	}
	stopID := uuid.NewString()
	final := 42.0
	view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: stopID, Sequence: 2, Position: &final})
	if err != nil {
		t.Fatal(err)
	}
	if view.Outcome != PlaybackOutcomeStopped || view.StopID != stopID || view.Accepted == nil || view.Accepted.Sequence != 2 || view.Accepted.Position != 42 {
		t.Fatalf("stop view = %+v", view)
	}
	if _, err := f.manager.GetSession(f.session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("session survived stop: %v", err)
	}
	// Any later stop, same or different id, replays the stored receipt.
	for _, id := range []string{stopID, uuid.NewString()} {
		again, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: id})
		if err != nil || again.Outcome != PlaybackOutcomeReplayed || again.StopID != stopID || again.Accepted == nil || again.Accepted.Position != 42 {
			t.Fatalf("replayed stop (%s) = %+v, %v", id, again, err)
		}
	}
	// Progress after stop is refused; the row is no longer live.
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 3, Position: 50})
	assertPlaybackOperationError(t, err, http.StatusNotFound, "session_not_found")
	// Malformed stop bodies.
	_, err = f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: "nope"})
	assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
	_, err = f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString(), Sequence: 5})
	assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
}

func TestNativePlaybackStopPublishesSavedProgressToProfile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		detached bool
	}{
		{name: "live session"},
		{name: "detached session", detached: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			store := newPlaybackTestStore(t)
			f.handler.StoreProvider = testUserStoreProvider{store: store}
			f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{
				ID: 100, ContentID: "series-1", EpisodeID: "episode-1", Duration: 3600,
			}}
			f.handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{SeriesID: "series-1"}}
			f.handler.EventsHub = evt.NewHub("playback-test", nil)
			events, unsubscribe := f.handler.EventsHub.Subscribe()
			t.Cleanup(unsubscribe)
			if tc.detached {
				if err := f.manager.StopSession(f.session.ID); err != nil {
					t.Fatal(err)
				}
			}

			_, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 600})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case event := <-events:
				t.Fatalf("position-only sample published %s", event.Event)
			default:
			}

			if !tc.detached {
				f.handler.sessionMgr = liveStopSnapshotRace{f.manager}
			}
			command := PlaybackStopCommand{StopID: "c2c87108-1bb1-4d1b-b25d-c37d928a9bd7", Sequence: 2, Position: new(900.0)}
			stopped, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, command)
			if err != nil || stopped.Outcome != PlaybackOutcomeStopped {
				t.Fatalf("stop: %+v, %v", stopped, err)
			}
			select {
			case event := <-events:
				var payload userStateEventPayload
				if err := jsonv2.Unmarshal(event.Data, &payload); err != nil {
					t.Fatal(err)
				}
				if event.Channel != evt.ChannelUserState || event.Event != userStateChangedEvent ||
					event.UserID != f.caller.UserID || event.ProfileID != f.caller.ProfileID ||
					payload.ProfileID != f.caller.ProfileID || payload.ContentID != "episode-1" ||
					payload.SeriesID != "series-1" || payload.Change != "progress" || !payload.PlaybackStopped {
					t.Fatalf("unexpected watch-state event: %+v, payload %+v", event, payload)
				}
			default:
				t.Fatal("saved playback stop did not publish a watch-state event")
			}
			saved, err := store.GetProgress(t.Context(), f.caller.ProfileID, "episode-1")
			if err != nil || saved == nil || saved.PositionSeconds != 900 {
				t.Fatalf("saved progress: %+v, %v", saved, err)
			}
			replayed, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, command)
			if err != nil || replayed.Outcome != PlaybackOutcomeReplayed {
				t.Fatalf("replay: %+v, %v", replayed, err)
			}
			select {
			case event := <-events:
				t.Fatalf("replayed stop published %s", event.Event)
			default:
			}
		})
	}
}

func TestCompatibilityPlaybackStopPublishesV2WatchState(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	f.handler.StoreProvider = testUserStoreProvider{store: newPlaybackTestStore(t)}
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{
		ID: 100, ContentID: "movie-1", Duration: 3600,
	}}
	f.handler.EventsHub = evt.NewHub("playback-test", nil)
	events, unsubscribe := f.handler.EventsHub.Subscribe()
	t.Cleanup(unsubscribe)

	session := &playback.Session{
		ID: f.session.ID, UserID: f.caller.UserID, ProfileID: f.caller.ProfileID,
		MediaFileID: 100, Position: 900, IsJellyfinCompat: true,
	}
	if result := f.handler.persistStopAndHistory(t.Context(), session); result.HistoryID == "" {
		t.Fatalf("compatibility stop did not write history: %+v", result)
	}
	select {
	case event := <-events:
		var payload userStateEventPayload
		if err := jsonv2.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if !event.V2Only || payload.ContentID != "movie-1" || !payload.PlaybackStopped {
			t.Fatalf("unexpected compatibility stop event: %+v, payload %+v", event, payload)
		}
	default:
		t.Fatal("compatibility stop did not publish a watch-state event")
	}
	if result := f.handler.persistStopAndHistory(t.Context(), session); !result.AlreadyRecorded {
		t.Fatalf("compatibility replay was recorded again: %+v", result)
	}
	select {
	case event := <-events:
		t.Fatalf("replayed compatibility stop published %s", event.Event)
	default:
	}
}

func TestSQLitePlaybackStopReplayPreservesLaterProgress(t *testing.T) {
	ctx := t.Context()
	store := newPlaybackTestStore(t)
	stop := userstore.PlaybackStopWrite{
		Entry: userstore.WatchHistoryEntry{
			ID: "final-stop", ProfileID: "profile-1", MediaItemID: "movie-1", DurationSeconds: 3600,
		},
		ReplayUntil:     time.Now().Add(time.Hour),
		PositionSeconds: 900,
	}
	if _, replay, err := userstore.CommitPlaybackStop(ctx, store, stop); err != nil || replay {
		t.Fatalf("initial stop: replay=%t, err=%v", replay, err)
	}
	if err := store.RemoveHistoryItems(ctx, "profile-1", []string{"movie-1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProgress(ctx, "profile-1", "movie-1", 1800, 3600, stop.Thresholds); err != nil {
		t.Fatal(err)
	}
	// A change to the threshold during receipt recovery cannot turn the
	// committed stop into a new completion.
	stop.Thresholds.WatchedPct = 10
	if _, replay, err := userstore.CommitPlaybackStop(ctx, store, stop); err != nil || !replay {
		t.Fatalf("retried stop: replay=%t, err=%v", replay, err)
	}
	progress, err := store.GetProgress(ctx, "profile-1", "movie-1")
	if err != nil || progress == nil || progress.PositionSeconds != 1800 {
		t.Fatalf("later progress: %+v, %v", progress, err)
	}
	history, err := store.ListHistory(ctx, "profile-1", 1, 0)
	if err != nil || len(history) != 0 {
		t.Fatalf("stop replay restored removed history: %+v, %v", history, err)
	}
}

func TestSQLitePlaybackProgressSkipsHintsBelowMinimumResume(t *testing.T) {
	ctx := t.Context()
	store := newPlaybackTestStore(t)
	thresholds := userstore.ProgressThresholds{MinResumePct: 10}
	if err := store.SetProgress(ctx, "profile-1", "movie-1", 900, 3600, thresholds); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateProgressHints(ctx, "profile-1", "movie-1", userstore.VersionHints{FileID: 1}); err != nil {
		t.Fatal(err)
	}
	written, completed, err := userstore.WritePlaybackProgress(ctx, store, "session-1", 1, "profile-1", "movie-1", 30, 3600, thresholds, userstore.VersionHints{FileID: 2})
	if err != nil || written || completed {
		t.Fatalf("skipped sample: written=%t, completed=%t, err=%v", written, completed, err)
	}
	progress, err := store.GetProgress(ctx, "profile-1", "movie-1")
	if err != nil || progress == nil || progress.PositionSeconds != 900 || progress.LastFileID == nil || *progress.LastFileID != 1 {
		t.Fatalf("skipped sample changed progress or hints: %+v, %v", progress, err)
	}
}

func TestNativePlaybackStopFinalizesAfterSenderDisconnects(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	store := newPlaybackTestStore(t)
	f.handler.StoreProvider = testUserStoreProvider{store: store}
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{
		ID: 100, ContentID: "movie-1", Duration: 3600,
	}}
	attempts := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
	record, err := f.handler.PlanStoreV3.GetAttempt(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, first, err := attempts.StopAttempt(t.Context(), f.session.ID, uuid.NewString(), &playback.ProgressSampleV3{Sequence: 1, Position: 900})
	if err != nil || !first {
		t.Fatalf("accept stop: %+v, %v", receipt, err)
	}
	disconnected, cancel := context.WithCancel(f.ctx)
	cancel()
	final := f.handler.finalizeStopV2(disconnected, attempts, record, f.session.ID, receipt)
	if !final.Finalized || final.HistoryID == "" {
		t.Fatalf("accepted stop did not finish: %+v", final)
	}
	if saved, err := store.GetProgress(t.Context(), f.caller.ProfileID, "movie-1"); err != nil || saved == nil || saved.PositionSeconds != 900 {
		t.Fatalf("saved progress: %+v, %v", saved, err)
	}
}

type liveStopSnapshotRace struct {
	*playback.SessionManager
}

func (m liveStopSnapshotRace) GetSession(id string) (*playback.Session, error) {
	session, err := m.SessionManager.GetSession(id)
	if err == nil && session.Position == 900 {
		// An accepted heartbeat finally reaches the live copy after the
		// stop applied 900 to it, but before teardown snapshots the copy.
		if updateErr := m.UpdateProgress(id, 600, false); updateErr != nil {
			return nil, updateErr
		}
		return m.SessionManager.GetSession(id)
	}
	return session, err
}

// TestStopPlaybackV2RefusesServerStopIDs covers a client stop that names one
// of the ids the server mints for its own ends: it is refused, so a client
// cannot record its play as terminated, expired or aborted.
func TestStopPlaybackV2RefusesServerStopIDs(t *testing.T) {
	for _, minted := range []func(string) string{adminStopID, expiredStopID, abortStopID, pausedTimeoutStopID} {
		f := newPlaybackServiceFixture(t)
		_, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: minted(f.session.ID)})
		assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
		if record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID); err != nil || record.StoppedAt != nil {
			t.Fatalf("attempt = %+v, %v; want it still live", record, err)
		}
	}
}

// TestStopPlaybackV2WithoutLiveSessionStillStopsTheRow covers a stop that
// reaches a replica without the session's copy: the row stops, and the admin
// log records the play from the attempt row, started when the attempt was.
//
// The play is measured from what its copy last saved, through the stop's own
// final position.
func TestStopPlaybackV2WithoutLiveSessionStillStopsTheRow(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	var saved playback.WatchTime
	saved.Observe(0, false, time.Now().Add(-15*time.Minute))
	admin := &savedWatchAdminStore{watch: saved}
	f.handler.AdminStore = admin
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
	if err := f.manager.StopSession(f.session.ID); err != nil {
		t.Fatal(err)
	}
	stopID := uuid.NewString()
	position := 900.0
	view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: stopID, Sequence: 1, Position: &position})
	if err != nil || view.Outcome != PlaybackOutcomeStopped || view.StopID != stopID {
		t.Fatalf("view = %+v, %v", view, err)
	}
	record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID)
	if err != nil || record.StoppedAt == nil {
		t.Fatalf("row not stopped: %+v %v", record, err)
	}
	if len(admin.history) != 1 {
		t.Fatalf("admin history = %+v, want one row", admin.history)
	}
	row := admin.history[0]
	if row.EndReason != PlaybackEndStopped || row.EndPositionSeconds != 900 || row.StartedAt != record.CreatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("admin row = reason %q at %v started %s, want stopped at 900 started %s", row.EndReason, row.EndPositionSeconds, row.StartedAt, record.CreatedAt)
	}
	if !row.Watch.Measured || row.Watch.StartPosition != 0 || row.Watch.PlayedSeconds != 900 || !row.Watch.Since.Equal(saved.Since) {
		t.Fatalf("admin watch = %+v, want the saved measurement through the final 900 s", row.Watch)
	}
}

// savedWatchAdminStore is a recording admin store whose sessions all have a
// saved measurement.
type savedWatchAdminStore struct {
	recordingPlaybackAdminStore
	watch playback.WatchTime
}

func (s *savedWatchAdminStore) SessionWatch(context.Context, string) (playback.WatchTime, bool, error) {
	return s.watch, s.watch.Measured, nil
}

// TestReplayedStopFinishesTheWinningStopsEnd covers a stop that replays an
// administrator stop whose finalizer never finished: the play is recorded
// as terminated, the end that won the row, not as the client's stop.
//
// An unfinished abort stays an abort: the admin log only, no watch history.
func TestReplayedStopFinishesTheWinningStopsEnd(t *testing.T) {
	for _, tc := range []struct {
		stopID      func(string) string
		reason      PlaybackEndReason
		watchedRows int
	}{
		{adminStopID, PlaybackEndTerminated, 1},
		{abortStopID, PlaybackEndAborted, 0},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			admin := &recordingPlaybackAdminStore{}
			userStore := newPlaybackTestStore(t)
			f.handler.AdminStore = admin
			f.handler.StoreProvider = testUserStoreProvider{store: userStore}
			f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
			if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
			if _, _, err := store.StopAttempt(context.Background(), f.session.ID, tc.stopID(f.session.ID), &playback.ProgressSampleV3{Sequence: 1, Position: 600}); err != nil {
				t.Fatal(err)
			}
			view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
			if err != nil || view.Outcome != PlaybackOutcomeReplayed {
				t.Fatalf("view = %+v, %v", view, err)
			}
			if len(admin.history) != 1 || admin.history[0].EndReason != tc.reason || admin.history[0].EndPositionSeconds != 600 {
				t.Fatalf("admin history = %+v, want one %s row at 600", admin.history, tc.reason)
			}
			if history, _ := userStore.ListHistory(t.Context(), "profile-1", 10, 0); len(history) != tc.watchedRows {
				t.Fatalf("watch history rows = %d, want %d", len(history), tc.watchedRows)
			}
		})
	}
}

func TestExpiredSessionMarksAttemptStopped(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	f.handler.handleExpiredSession(f.session)
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID)
		if err == nil && record.StoppedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("attempt row never stopped after expiry: %+v %v", record, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A replayed stop reports the server-minted identity.
	view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
	if err != nil || view.Outcome != PlaybackOutcomeReplayed || view.StopID != expiredStopID(f.session.ID) {
		t.Fatalf("view = %+v, %v", view, err)
	}
}

// TestProgressSideEffectsNeverOverwriteANewerSample drives the race the
// row's compare-and-set alone does not close: sequence 1 wins its CAS, then
// sequence 2 is applied and persisted before sequence 1's side effects run.
// The live session must keep sequence 2's position.
func TestProgressSideEffectsNeverOverwriteANewerSample(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
	record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := playback.ProgressSampleV3{Sequence: 1, Position: 600}
	if _, err := store.ApplyProgress(context.Background(), f.session.ID, first); err != nil {
		t.Fatal(err)
	}
	// Sequence 2 completes end to end while sequence 1 is still "in flight".
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 2, Position: 1200}); err != nil {
		t.Fatal(err)
	}
	// Sequence 1's side effects now run late; they must observe the newer row
	// and do nothing.
	f.handler.persistProgressV2(f.ctx, store, record, f.session.ID, first)
	live, err := f.manager.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.Position != 1200 {
		t.Fatalf("live position = %v, want 1200 (older sample overwrote a newer one)", live.Position)
	}
}

// progressWriteInterceptor is a user store whose UpdateProgress runs a hook
// before the first write of a given position lands, so a test can wedge
// another replica's writes between this replica's row check and its write.
type progressWriteInterceptor struct {
	userstore.UserStore
	beforePosition float64
	before         func()
	fired          bool
}

func (s *progressWriteInterceptor) UpdateProgress(ctx context.Context, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	if !s.fired && position == s.beforePosition {
		s.fired = true
		s.before()
	}
	return s.UserStore.UpdateProgress(ctx, profileID, mediaItemID, position, duration, thresholds)
}

// TestProgressSideEffectsAcrossReplicasEndAtTheRowsLatestSample drives the
// two-replica race a process-local lock cannot order: replica A passes its
// row check for sequence 1, replica B applies and persists sequence 2 end to
// end, then A's user-store write lands. A must notice the row moved and
// leave the resume position (and its live session) at sequence 2.
func TestProgressSideEffectsAcrossReplicasEndAtTheRowsLatestSample(t *testing.T) {
	userStore := newPlaybackTestStore(t)
	file := &models.MediaFile{ID: 100, ContentID: "book-1", Duration: 3600}
	planStore := playback.NewMemoryPlanStoreV3()
	ctx := newAuthorizedPlaybackContext()
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}

	replica := func(manager *playback.SessionManager, store userstore.UserStore) *PlaybackHandler {
		h := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
		h.InstallationID = serviceInstallation
		h.PlanStoreV3 = planStore
		h.StoreProvider = testUserStoreProvider{store: store}
		return h
	}
	managerA := playback.NewSessionManager(0, 0)
	session, err := managerA.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := planStore.SaveAttempt(context.Background(), playback.AttemptRecordV3{
		PlaybackAttemptID: uuid.NewString(), SessionID: session.ID, UserID: 1, ProfileID: "profile-1",
		RequestedMediaFileID: file.ID, EffectiveMediaFileID: file.ID, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// Replica B never held the session; it writes from the attempt row.
	replicaB := replica(playback.NewSessionManager(0, 0), userStore)
	interceptor := &progressWriteInterceptor{UserStore: userStore, beforePosition: 600, before: func() {
		if _, err := replicaB.ApplyProgressV2(ctx, caller, session.ID, PlaybackProgressCommand{Sequence: 2, Position: 1200}); err != nil {
			t.Errorf("replica B progress: %v", err)
		}
	}}
	replicaA := replica(managerA, interceptor)

	view, err := replicaA.ApplyProgressV2(ctx, caller, session.ID, PlaybackProgressCommand{Sequence: 1, Position: 600})
	if err != nil || view.Outcome != PlaybackOutcomeApplied {
		t.Fatalf("replica A view = %+v, %v", view, err)
	}
	if !interceptor.fired {
		t.Fatal("precondition: replica B did not run between A's check and A's write")
	}
	saved, err := userStore.GetProgress(context.Background(), "profile-1", "book-1")
	if err != nil || saved == nil {
		t.Fatalf("saved progress = %+v, %v", saved, err)
	}
	if saved.PositionSeconds != 1200 {
		t.Fatalf("saved position = %v, want 1200 (older sample overwrote a newer one across replicas)", saved.PositionSeconds)
	}
	live, err := managerA.GetSession(session.ID)
	if err != nil || live.Position != 1200 {
		t.Fatalf("live position = %+v, %v; want 1200", live, err)
	}
}

// TestReplayedStopFinishesAnUnfinalizedStop models the winner dying between
// the row CAS and its side effects: the row is stopped but the live session
// survived. The replay must finish the job rather than answer "replayed" over
// a session that is still serving.
func TestReplayedStopFinishesAnUnfinalizedStop(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
	stopID := uuid.NewString()
	if _, first, err := store.StopAttempt(context.Background(), f.session.ID, stopID, nil); err != nil || !first {
		t.Fatalf("seed stop: first=%v err=%v", first, err)
	}
	if _, err := f.manager.GetSession(f.session.ID); err != nil {
		t.Fatalf("precondition: live session should have survived the interrupted stop: %v", err)
	}
	view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
	if err != nil || view.Outcome != PlaybackOutcomeReplayed || view.StopID != stopID {
		t.Fatalf("view = %+v, %v", view, err)
	}
	if _, err := f.manager.GetSession(f.session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("replay left the live session serving: %v", err)
	}
	receipt, first, err := store.StopAttempt(context.Background(), f.session.ID, uuid.NewString(), nil)
	if err != nil || first || !receipt.Finalized {
		t.Fatalf("receipt after replay = %+v first=%v err=%v", receipt, first, err)
	}
}

// TestExpiryDefersToAStoppedOrActiveRow: a replica reaping a stale local
// copy must not finalize an attempt another replica stopped or is still
// feeding progress.
func TestExpiryDefersToAStoppedOrActiveRow(t *testing.T) {
	t.Run("already stopped elsewhere", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
			remoteStop := uuid.NewString()
			if _, _, err := store.StopAttempt(context.Background(), f.session.ID, remoteStop, &playback.ProgressSampleV3{Sequence: 9, Position: 900}); err != nil {
				t.Fatal(err)
			}
			f.handler.handleExpiredSession(f.session)
			synctest.Wait()
			receipt, _, err := store.StopAttempt(context.Background(), f.session.ID, uuid.NewString(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.StopID != remoteStop || receipt.Accepted == nil || receipt.Accepted.Position != 900 {
				t.Fatalf("expiry overwrote the remote stop: %+v", receipt)
			}
		})
	})
	t.Run("active elsewhere", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
			// The local copy went idle before the row's last accepted sample.
			f.session.LastActivityAt = time.Now().Add(-time.Hour)
			if _, err := store.ApplyProgress(context.Background(), f.session.ID, playback.ProgressSampleV3{Sequence: 1, Position: 30}); err != nil {
				t.Fatal(err)
			}
			f.handler.handleExpiredSession(f.session)
			synctest.Wait()
			record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if record.StoppedAt != nil {
				t.Fatalf("expiry stopped an attempt that is live on another replica: %+v", record)
			}
		})
	})
}

func TestSubtitleFontServiceReconstructsAndChecksAdmission(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	playbackHandler := newKeyedPlaybackHandler(manager)
	playbackHandler.JWTSecret = "font-service-secret"
	file := &models.MediaFile{ID: 100, FilePath: writePlaybackTestMediaFile(t, "font-source.mkv"),
		SubtitleTracks: []models.SubtitleTrack{{Index: 4, Codec: "subrip"}},
	}
	stream := NewStreamHandler(manager, testPlaybackFileResolver{file: file})
	stream.JWTSecret = playbackHandler.JWTSecret
	stream.TM = playbackHandler.TranscodeManager()
	sessionID := uuid.NewString()
	card := playback.NewDirectRecipeCard(sessionID, 1, "profile-1", 100)
	token := playbackHandler.signStreamClaims(card.ToClaims())
	request := SubtitleFontRequest{SessionID: sessionID, Track: "0", Query: url.Values{streamTokenParam: {token}}}
	ctx := apimw.SetProfileID(newAuthorizedPlaybackContext(), "profile-1")
	// Reaching the codec refusal proves the service reconstructed and loaded
	// the requested file without running the HTTP font handler.
	_, err := stream.SubtitleFonts(ctx, request)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != http.StatusBadRequest || !strings.Contains(failure.Message, "ASS/SSA") {
		t.Fatalf("reconstruction did not reach font selection: %v", err)
	}
	if _, err := manager.GetSession(sessionID); err != nil {
		t.Fatalf("session was not registered after reconstruction: %v", err)
	}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		query  url.Values
		status int
	}{
		{"anonymous", context.Background(), request.Query, http.StatusUnauthorized},
		{"other profile", apimw.SetProfileID(ctx, "profile-2"), request.Query, http.StatusForbidden},
		{"other account", apimw.SetClaims(ctx, &auth.Claims{UserID: 2}), request.Query, http.StatusForbidden},
		{"unrelated file", ctx, url.Values{"file_id": {"200"}}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := stream.SubtitleFonts(tc.ctx, SubtitleFontRequest{SessionID: sessionID, Track: "0", Query: tc.query})
			if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != tc.status {
				t.Fatalf("admission failure = %v, want %d", err, tc.status)
			}
		})
	}
	if err := os.Remove(file.FilePath); err != nil {
		t.Fatal(err)
	}
	_, err = stream.SubtitleFonts(ctx, request)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != http.StatusNotFound {
		t.Fatalf("missing source file = %v, want 404", err)
	}
	if _, err := manager.GetSession(sessionID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("missing source left session live: %v", err)
	}
}

// TestReplayedProgressRedoesPersistence: a retried identical sample means
// the first reply was lost, possibly before the side effects ran, so the
// replay persists again rather than trusting the earlier attempt.
func TestReplayedProgressRedoesPersistence(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
	// The CAS committed but the side effects never ran (no live update).
	if _, err := store.ApplyProgress(context.Background(), f.session.ID, playback.ProgressSampleV3{Sequence: 1, Position: 250}); err != nil {
		t.Fatal(err)
	}
	if live, _ := f.manager.GetSession(f.session.ID); live.Position == 250 {
		t.Fatal("precondition: live session already at 250")
	}
	view, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 250})
	if err != nil || view.Outcome != PlaybackOutcomeReplayed {
		t.Fatalf("view = %+v, %v", view, err)
	}
	live, err := f.manager.GetSession(f.session.ID)
	if err != nil || live.Position != 250 {
		t.Fatalf("replayed sample was not persisted: %+v %v", live, err)
	}
}

// TestStopFinalizationRunsOnce: two replays of an unfinalized stop must
// produce one history writer run, not one per replay. A replay that loses
// the claim waits for the holder, bounded by its request context, and takes
// over only once the holder's lease has lapsed.
func TestStopFinalizationRunsOnce(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
	stopID := uuid.NewString()
	if _, _, err := store.StopAttempt(context.Background(), f.session.ID, stopID, nil); err != nil {
		t.Fatal(err)
	}
	// Hold the claim as a dead replica would, then replay under a bounded
	// request: the replay must not finalize while the lease is live, and
	// must report the stored receipt when its context ends.
	if won, err := store.ClaimStopFinalization(context.Background(), f.session.ID, time.Now().Add(time.Minute)); err != nil || !won {
		t.Fatalf("seed claim: %v %v", won, err)
	}
	bounded, cancel := context.WithTimeout(f.ctx, 200*time.Millisecond)
	defer cancel()
	view, err := f.handler.StopPlaybackV2(bounded, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
	if err != nil || view.Outcome != PlaybackOutcomeReplayed || view.StopID != stopID {
		t.Fatalf("view = %+v, %v", view, err)
	}
	if _, err := f.manager.GetSession(f.session.ID); err != nil {
		t.Fatalf("a replay that lost the claim must not tear the session down: %v", err)
	}
	if replay, _, _ := store.StopAttempt(context.Background(), f.session.ID, uuid.NewString(), nil); replay.Finalized {
		t.Fatalf("a replay that lost the claim finalized the receipt: %+v", replay)
	}
	// Once the lease lapses the next replay finishes the job exactly once.
	// Rewrite the stored receipt with an already-expired lease, as time would.
	receipt, _, _ := store.StopAttempt(context.Background(), f.session.ID, uuid.NewString(), nil)
	receipt.FinalizingUntil = time.Now().Add(-time.Second)
	if err := store.RecordStopReceipt(context.Background(), f.session.ID, receipt); err != nil {
		t.Fatal(err)
	}
	view, err = f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
	if err != nil || view.Outcome != PlaybackOutcomeReplayed {
		t.Fatalf("second replay = %+v, %v", view, err)
	}
	if _, err := f.manager.GetSession(f.session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("finalization did not run after the lease lapsed: %v", err)
	}
	finalized, _, _ := store.StopAttempt(context.Background(), f.session.ID, uuid.NewString(), nil)
	if !finalized.Finalized {
		t.Fatalf("receipt not finalized: %+v", finalized)
	}
}

// An attempt an administrator or the paused timeout ended answers 410 on
// progress and replan, so the player exits: the paused timeout with its own
// type for a client that advertised paused_timeout_v1. An expired attempt
// keeps answering 404 so the player renews. Another profile gets 403 either
// way.
func TestStoppedAttemptAnswersGoneWhenThePlayerMustLeave(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stopID   func(string) string
		features []string
		status   int
		code     string
	}{
		{"administrator", adminStopID, nil, http.StatusGone, "playback_session_ended"},
		{"paused timeout", pausedTimeoutStopID, []string{playback.FeaturePausedTimeoutV3}, http.StatusGone, "playback_session_paused_timeout"},
		{"paused timeout, legacy client", pausedTimeoutStopID, nil, http.StatusGone, "playback_session_ended"},
		{"expiry", expiredStopID, []string{playback.FeaturePausedTimeoutV3}, http.StatusNotFound, "session_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			record := f.withClientFeatures(t, tc.features...)
			stopAttemptAs(f.handler, f.session.ID, tc.stopID(f.session.ID))
			_, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10})
			assertPlaybackOperationError(t, err, tc.status, tc.code)
			replan := PlaybackReplanCommand{Request: playback.ReplanRequestV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: record.PlaybackAttemptID, ReplanRequestID: "replan-0123456789", FailedPlanID: "plan-0123456789", PlanAttemptID: "plan-attempt-0123", PlanAttemptKey: "v3:0123456789abcdef", AttemptCount: 1, QualityPreference: "auto"}, Digest: "d"}
			_, err = f.handler.ReplanPlaybackV2(f.ctx, f.caller, f.session.ID, replan)
			assertPlaybackOperationError(t, err, tc.status, tc.code)
			// Another profile is refused before it learns how the play ended.
			other := f.caller
			other.ProfileID = "profile-2"
			otherCtx := apimw.SetProfileID(f.ctx, other.ProfileID)
			_, err = f.handler.ApplyProgressV2(otherCtx, other, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10})
			assertPlaybackOperationError(t, err, http.StatusForbidden, "forbidden")
			_, err = f.handler.ReplanPlaybackV2(otherCtx, other, f.session.ID, replan)
			assertPlaybackOperationError(t, err, http.StatusForbidden, "forbidden")
		})
	}
}

// A client that stopped first keeps its own receipt: a later administrator
// stop of the same attempt does not turn its progress into 410.
func TestAdministratorStopKeepsAClientStopReceipt(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	stopID := uuid.NewString()
	if _, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: stopID}); err != nil {
		t.Fatal(err)
	}
	stopAttemptAs(f.handler, f.session.ID, adminStopID(f.session.ID))
	record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID)
	if err != nil || record.StopID != stopID {
		t.Fatalf("record = %+v, %v", record, err)
	}
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10})
	assertPlaybackOperationError(t, err, http.StatusNotFound, "session_not_found")
}

// stopAfterFirstReadPlanStoreV3 runs stop once the first attempt read
// returns, the window between the replan precheck and the replan itself.
type stopAfterFirstReadPlanStoreV3 struct {
	*playback.MemoryPlanStoreV3
	stop func()
	done bool
}

func (s *stopAfterFirstReadPlanStoreV3) GetAttempt(ctx context.Context, sessionID string) (*playback.AttemptRecordV3, error) {
	record, err := s.MemoryPlanStoreV3.GetAttempt(ctx, sessionID)
	if !s.done {
		s.done = true
		s.stop()
	}
	return record, err
}

// An administrator stop or a paused timeout that lands after the replan
// precheck still answers 410, not the 404 the player would renew from.
func TestReplanAnswersGoneWhenAnEndLandsMidReplan(t *testing.T) {
	for _, tc := range []struct {
		stopID func(string) string
		code   string
	}{
		{adminStopID, "playback_session_ended"},
		{pausedTimeoutStopID, "playback_session_paused_timeout"},
	} {
		f := newPlaybackServiceFixture(t)
		start := v3HandlerStartRequest()
		memory, ok := f.handler.PlanStoreV3.(*playback.MemoryPlanStoreV3)
		if !ok {
			t.Fatalf("plan store = %T", f.handler.PlanStoreV3)
		}
		record := f.withClientFeatures(t, playback.FeaturePausedTimeoutV3)
		f.handler.PlanStoreV3 = &stopAfterFirstReadPlanStoreV3{MemoryPlanStoreV3: memory, stop: func() {
			if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			stopAttemptAs(f.handler, f.session.ID, tc.stopID(f.session.ID))
		}}
		_, err := f.handler.ReplanPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackReplanCommand{Request: playback.ReplanRequestV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: record.PlaybackAttemptID, ReplanRequestID: "replan-0123456789", FailedPlanID: "plan-0123456789", PlanAttemptID: "plan-attempt-0123", PlanAttemptKey: "v3:0123456789abcdef", AttemptCount: 1, QualityPreference: "auto", Failure: playback.FailureV3{Classification: "network_error"}, Capabilities: start.Capabilities, ClientPlaybackContext: start.ClientPlaybackContext}, Digest: "d"})
		assertPlaybackOperationError(t, err, http.StatusGone, tc.code)
	}
}

// withClientFeatures sets the fixture attempt's durable client features and
// returns the attempt.
func (f *playbackServiceFixture) withClientFeatures(t *testing.T, features ...string) *playback.AttemptRecordV3 {
	t.Helper()
	memory, ok := f.handler.PlanStoreV3.(*playback.MemoryPlanStoreV3)
	if !ok {
		t.Fatalf("plan store = %T", f.handler.PlanStoreV3)
	}
	record, err := memory.GetAttempt(context.Background(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.NormalizedRequest.ClientFeatures = features
	memory.ReplaceAttempt(context.Background(), *record)
	return record
}

// TestStaleCopyExpiryLeavesTheRemoteTransport: a replica reaping a stale copy
// of a session another replica is still serving must not stop the remote
// transport. The node fences a stopped transport against reconstruction, so a
// stop here would end the other replica's playback when the user resumes. A
// copy that is really over, here or on another replica, still stops it.
func TestStaleCopyExpiryLeavesTheRemoteTransport(t *testing.T) {
	expire := func(t *testing.T, stale, stoppedElsewhere bool) []string {
		t.Helper()
		var mu sync.Mutex
		var deletes []string
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				mu.Lock()
				deletes = append(deletes, r.URL.Path)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(node.Close)

		f := newPlaybackServiceFixture(t)
		session := *f.session
		session.TranscodeNodeURL = node.URL
		session.TranscodeTransportID = session.ID + "-plan1234-abcd1234"
		if stale {
			// The local copy went idle before the row's last accepted sample.
			session.LastActivityAt = time.Now().Add(-time.Hour)
			store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
			if _, err := store.ApplyProgress(context.Background(), session.ID, playback.ProgressSampleV3{Sequence: 1, Position: 30}); err != nil {
				t.Fatal(err)
			}
		}
		if stoppedElsewhere {
			// Another replica recorded the stop without knowing this copy's
			// transport.
			stopAttemptAs(f.handler, session.ID, expiredStopID(session.ID))
		}
		f.handler.expireSessionCopy(context.Background(), &session)
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(deletes)
	}

	t.Run("stale copy", func(t *testing.T) {
		if got := expire(t, true, false); len(got) != 0 {
			t.Fatalf("stale-copy expiry stopped the remote transport another replica serves: %v", got)
		}
	})
	stopsTransport := func(paths []string) bool {
		return slices.ContainsFunc(paths, func(path string) bool { return strings.HasSuffix(path, "-plan1234-abcd1234") })
	}
	t.Run("expired session", func(t *testing.T) {
		if got := expire(t, false, false); !stopsTransport(got) {
			t.Fatalf("expiry of an idle session did not stop its remote transport: %v", got)
		}
	})
	t.Run("stopped elsewhere", func(t *testing.T) {
		if got := expire(t, false, true); !stopsTransport(got) {
			t.Fatalf("expiry of a copy stopped on another replica did not stop its remote transport: %v", got)
		}
	})
}

// TestSidecarRoutesEnforceOwningProfile: a household profile must not read
// another profile's sidecars or HLS by session id. The font service's check
// is TestSubtitleFontServiceReconstructsAndChecksAdmission.
func TestSidecarRoutesEnforceOwningProfile(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 100, playback.PlayTranscode, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := newKeyedPlaybackHandler(manager)
	stream := NewStreamHandler(manager, testPlaybackFileResolver{})
	stream.TM = handler.TranscodeManager()
	params := map[string]string{"session_id": session.ID, "track": "0", "name": "segment0.m4s"}
	for name, serve := range map[string]http.HandlerFunc{
		"manifest": handler.HandleGetTranscodeManifest,
		"segment":  handler.HandleGetTranscodeSegment,
		"stream":   stream.HandleStream,
		"subtitle": stream.HandleSubtitle,
	} {
		req := playbackTestRequest(http.MethodGet, "/", nil, params)
		req = req.WithContext(apimw.SetProfileID(req.Context(), "profile-2"))
		rr := httptest.NewRecorder()
		serve(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403 for another profile; body = %s", name, rr.Code, rr.Body.String())
		}
	}
}

// TestSidecarRoutesReconstructFromTheStreamReference: after a restart the
// subtitle URL carries the same signed reference as the media URL and must
// rebuild the session from it instead of answering 404. The font service's
// reconstruction is TestSubtitleFontServiceReconstructsAndChecksAdmission.
func TestSidecarRoutesReconstructFromTheStreamReference(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager)
	handler.JWTSecret = "sidecar-secret"
	handler.NodeKeys = nodeauthtest.SameKey("sidecar-secret")
	stream := NewStreamHandler(manager, testPlaybackFileResolver{})
	stream.JWTSecret = handler.JWTSecret
	stream.TM = handler.TranscodeManager()
	sessionID := uuid.NewString()
	card := playback.NewDirectRecipeCard(sessionID, 1, "profile-1", 100)
	token := handler.signStreamClaims(card.ToClaims())
	if token == "" {
		t.Fatal("no stream token")
	}
	params := map[string]string{"session_id": sessionID, "track": "0"}
	for name, serve := range map[string]http.HandlerFunc{
		"subtitle": stream.HandleSubtitle,
	} {
		rr := httptest.NewRecorder()
		serve(rr, playbackTestRequest(http.MethodGet, "/?"+streamTokenParam+"="+token, nil, params))
		if rr.Code == http.StatusNotFound && strings.Contains(rr.Body.String(), "session") {
			t.Fatalf("%s: session was not reconstructed: %d %s", name, rr.Code, rr.Body.String())
		}
		if _, err := manager.GetSession(sessionID); err != nil {
			t.Fatalf("%s: session not registered after reconstruction: %v", name, err)
		}
	}
}

// Progress and stop act on an attempt only for the profile that owns it: a
// sibling profile on the same account, or another account, is refused and
// leaves the session as it was.
func TestPlaybackMutationsV2RequireTheOwningProfile(t *testing.T) {
	for _, test := range []struct {
		name      string
		stop      bool
		userID    int
		profileID string
		refused   bool
	}{
		{name: "progress from another account", userID: 2, profileID: "profile-1", refused: true},
		{name: "progress from a sibling profile", userID: 1, profileID: "profile-2", refused: true},
		{name: "progress from the owning profile", userID: 1, profileID: "profile-1"},
		{name: "stop from another account", stop: true, userID: 2, profileID: "profile-1", refused: true},
		{name: "stop from a sibling profile", stop: true, userID: 1, profileID: "profile-2", refused: true},
		{name: "stop from the owning profile", stop: true, userID: 1, profileID: "profile-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			caller := f.caller
			caller.UserID, caller.ProfileID = test.userID, test.profileID
			ctx := playbackCallerContextV3(test.userID, test.profileID)
			var err error
			if test.stop {
				_, err = f.handler.StopPlaybackV2(ctx, caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
			} else {
				_, err = f.handler.ApplyProgressV2(ctx, caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 42})
			}
			session, getErr := f.manager.GetSession(f.session.ID)
			switch {
			case test.refused:
				assertPlaybackOperationError(t, err, http.StatusForbidden, "forbidden")
				if getErr != nil || session.Position != 0 {
					t.Fatalf("refused caller changed the session: %+v, %v", session, getErr)
				}
			case err != nil:
				t.Fatalf("owner mutation: %v", err)
			case test.stop:
				if !errors.Is(getErr, playback.ErrSessionNotFound) {
					t.Fatalf("owner stop left the session: %+v, %v", session, getErr)
				}
			default:
				if getErr != nil || session.Position != 42 {
					t.Fatalf("owner progress not applied: %+v, %v", session, getErr)
				}
			}
		})
	}
}

type nativeEndRaceCase struct {
	name          string
	end           func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3)
	wantReason    PlaybackEndReason
	wantWatched   float64
	wantProviders int
	// noWatchHistory: the end is an abort, which writes no watch history.
	noWatchHistory bool
}

// nativeEndRaceCases are TestNativeEndWritesOnceWhoeverWinsTheRow's ends.
func nativeEndRaceCases(t *testing.T) []nativeEndRaceCase {
	clientStopMidway := func(f *playbackServiceFixture) func() {
		return func() {
			if _, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return append([]nativeEndRaceCase{{
		name: "expiry loses to a stop that lands after its checks",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			// The reaper already removed this copy.
			if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			f.handler.PlanStoreV3 = &stopAfterReadsPlanStoreV3{MemoryPlanStoreV3: memory, after: 2, stop: clientStopMidway(f)}
			f.handler.expireSessionCopy(context.Background(), f.session)
		},
		wantReason: PlaybackEndStopped, wantWatched: 900, wantProviders: 1,
	}, {
		name: "abort loses to a client stop",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			f.handler.PlanStoreV3 = &stopAfterReadsPlanStoreV3{MemoryPlanStoreV3: memory, stop: clientStopMidway(f)}
			if err := f.handler.abortPlaybackSession(context.Background(), f.session); err != nil {
				t.Fatal(err)
			}
		},
		wantReason: PlaybackEndStopped, wantWatched: 900, wantProviders: 1,
	}, {
		name: "abort finishes a client stop that never claimed its end",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			if _, _, err := memory.StopAttempt(context.Background(), f.session.ID, uuid.NewString(), &playback.ProgressSampleV3{Sequence: 2, Position: 1200}); err != nil {
				t.Fatal(err)
			}
			if err := f.handler.abortPlaybackSession(context.Background(), f.session); err != nil {
				t.Fatal(err)
			}
		},
		wantReason: PlaybackEndStopped, wantWatched: 1200, wantProviders: 1,
	}, {
		name: "stream abort finishes a client stop that never claimed its end",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			if _, _, err := memory.StopAttempt(context.Background(), f.session.ID, uuid.NewString(), &playback.ProgressSampleV3{Sequence: 2, Position: 1200}); err != nil {
				t.Fatal(err)
			}
			stream := NewStreamHandler(f.manager, f.handler.fileResolver)
			stream.PlanStoreV3, stream.FinishOtherEnd = memory, f.handler.FinishOtherEnd
			stream.abortPlaybackSession(context.Background(), f.session)
			if record, _ := memory.GetAttempt(context.Background(), f.session.ID); record == nil {
				t.Fatal("attempt missing")
			}
			if receipt, _, err := memory.StopAttempt(context.Background(), f.session.ID, uuid.NewString(), nil); err != nil || !receipt.Finalized {
				t.Fatalf("receipt = %+v, %v; want the client stop finalized", receipt, err)
			}
		},
		wantReason: PlaybackEndStopped, wantWatched: 1200, wantProviders: 1,
	}, {
		name: "expiry records the sample its attempt accepted",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			snapshot, _ := f.manager.GetSession(f.session.ID)
			if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			// Progress another replica accepted after this copy went idle.
			if _, err := memory.ApplyProgress(context.Background(), f.session.ID, playback.ProgressSampleV3{Sequence: 2, Position: 1500}); err != nil {
				t.Fatal(err)
			}
			snapshot.LastActivityAt = time.Now().Add(time.Hour) // not stale relative to the row
			f.handler.expireSessionCopy(context.Background(), snapshot)
		},
		wantReason: PlaybackEndExpired, wantWatched: 1500, wantProviders: 1,
	}}, append(nativeTerminateRaceCases(t), nativeTakeoverRaceCases(t)...)...)
}

// nativeTerminateRaceCases are the administrator ends among
// nativeEndRaceCases.
func nativeTerminateRaceCases(t *testing.T) []nativeEndRaceCase {
	return []nativeEndRaceCase{{
		name: "terminate on a replica without the copy",
		end: func(f *playbackServiceFixture, _ *playback.MemoryPlanStoreV3) {
			if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.handler.endSessionAsAdmin(context.Background(), f.session.ID); err != nil {
				t.Fatal(err)
			}
		},
		wantReason: PlaybackEndTerminated, wantWatched: 900, wantProviders: 1,
	}, {
		name: "terminate whose claim reply was lost",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			f.handler.PlanStoreV3 = &lostClaimReplyPlanStoreV3{MemoryPlanStoreV3: memory}
			if err := f.handler.endSessionAsAdmin(context.Background(), f.session.ID); err != nil {
				t.Fatal(err)
			}
			// The end is finalized: a later stop replays it and writes nothing.
			if view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()}); err != nil || view.Outcome != PlaybackOutcomeReplayed {
				t.Fatalf("later stop = %+v, %v", view, err)
			}
		},
		wantReason: PlaybackEndTerminated, wantWatched: 900, wantProviders: 1,
	}}
}

// nativeTakeoverRaceCases are the ends among nativeEndRaceCases that find the
// row stopped by an end whose replica died before finishing it, and finish
// it as that end.
func nativeTakeoverRaceCases(t *testing.T) []nativeEndRaceCase {
	stoppedBy := func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3, stopID string, dropCopy bool) {
		if _, _, err := memory.StopAttempt(context.Background(), f.session.ID, stopID, nil); err != nil {
			t.Fatal(err)
		}
		if !dropCopy {
			return
		}
		if err := f.manager.StopSession(f.session.ID); err != nil {
			t.Fatal(err)
		}
	}
	terminate := func(f *playbackServiceFixture) {
		if err := f.handler.endSessionAsAdmin(context.Background(), f.session.ID); err != nil {
			t.Fatal(err)
		}
	}
	return []nativeEndRaceCase{{
		name: "terminate without the copy finishes an abort",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			stoppedBy(f, memory, abortStopID(f.session.ID), true)
			terminate(f)
		},
		wantReason: PlaybackEndAborted, wantWatched: 900, wantProviders: 1, noWatchHistory: true,
	}, {
		name: "terminate with the copy finishes an abort",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			stoppedBy(f, memory, abortStopID(f.session.ID), false)
			terminate(f)
		},
		wantReason: PlaybackEndAborted, wantWatched: 900, wantProviders: 1, noWatchHistory: true,
	}, {
		name: "expiry finishes an expiry",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			snapshot, _ := f.manager.GetSession(f.session.ID)
			stoppedBy(f, memory, expiredStopID(f.session.ID), true) // The reaper already removed this copy.
			f.handler.expireSessionCopy(context.Background(), snapshot)
		},
		wantReason: PlaybackEndExpired, wantWatched: 900, wantProviders: 1,
	}, {
		name: "expiry of a finished stop whose claim cannot read the row",
		end: func(f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			snapshot, _ := f.manager.GetSession(f.session.ID)
			if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			f.handler.PlanStoreV3 = &failingReadsPlanStoreV3{MemoryPlanStoreV3: memory, okReads: 1}
			f.handler.expireSessionCopy(context.Background(), snapshot)
		},
		wantReason: PlaybackEndStopped, wantWatched: 900, wantProviders: 1,
	}}
}

// TestNativeEndWritesOnceWhoeverWinsTheRow covers ends that race on one
// attempt row: only the end that wins the row and its finalization records
// the play, with the winning stop's own writers, reason and final sample.
// The loser adds no watch-history or admin row, and an end on a replica
// without the live copy still records the play and tells providers.
func TestNativeEndWritesOnceWhoeverWinsTheRow(t *testing.T) {
	for _, tc := range nativeEndRaceCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			admin := &recordingPlaybackAdminStore{}
			userStore := newPlaybackTestStore(t)
			scrobbler := &recordingPlaybackWatchScrobbler{}
			f.handler.AdminStore = admin
			f.handler.WatchScrobbler = scrobbler
			f.handler.StoreProvider = testUserStoreProvider{store: userStore}
			f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
			if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 900}); err != nil {
				t.Fatal(err)
			}
			memory := f.handler.PlanStoreV3.(*playback.MemoryPlanStoreV3)
			tc.end(f, memory)

			if len(admin.history) != 1 || admin.history[0].EndReason != tc.wantReason || admin.history[0].EndPositionSeconds != tc.wantWatched {
				t.Fatalf("admin history = %+v, want one %s row at %v", admin.history, tc.wantReason, tc.wantWatched)
			}
			history, err := userStore.ListHistory(t.Context(), "profile-1", 10, 0)
			wantRows := 1
			if tc.noWatchHistory {
				wantRows = 0
			}
			if err != nil || len(history) != wantRows {
				t.Fatalf("watch history = %+v, %v; want %d rows", history, err, wantRows)
			}
			if got := len(scrobbler.stops) + len(scrobbler.pauses); got != tc.wantProviders {
				t.Fatalf("provider end events = %d stops + %d pauses, want %d", len(scrobbler.stops), len(scrobbler.pauses), tc.wantProviders)
			}
			if tc.noWatchHistory && (len(scrobbler.pauses) != 1 || !scrobbler.pauses[0].SessionEnded) {
				t.Fatalf("abort provider events = %+v, want one pause that ends the session", scrobbler.pauses)
			}
			record, err := memory.GetAttempt(context.Background(), f.session.ID)
			if err != nil || record.StoppedAt == nil {
				t.Fatalf("attempt = %+v, %v; want it stopped", record, err)
			}
		})
	}
}

// failingReadsPlanStoreV3 fails every attempt read after the first okReads,
// as a store that stops answering would.
type failingReadsPlanStoreV3 struct {
	*playback.MemoryPlanStoreV3
	okReads int
}

func (s *failingReadsPlanStoreV3) GetAttempt(ctx context.Context, sessionID string) (*playback.AttemptRecordV3, error) {
	if s.okReads == 0 {
		return nil, errors.New("store unavailable")
	}
	s.okReads--
	return s.MemoryPlanStoreV3.GetAttempt(ctx, sessionID)
}

// lostClaimReplyPlanStoreV3 commits the first finalization claim but loses
// its reply, as a dropped connection would.
type lostClaimReplyPlanStoreV3 struct {
	*playback.MemoryPlanStoreV3
	lost bool
}

func (s *lostClaimReplyPlanStoreV3) ClaimStopFinalization(ctx context.Context, sessionID string, lease time.Time) (bool, error) {
	claimed, err := s.MemoryPlanStoreV3.ClaimStopFinalization(ctx, sessionID, lease)
	if !s.lost {
		s.lost = true
		return false, errors.New("reply lost")
	}
	return claimed, err
}

// stopAfterReadsPlanStoreV3 lands another stop once the attempt has been
// read `after` times, between an end's checks and its claim.
type stopAfterReadsPlanStoreV3 struct {
	*playback.MemoryPlanStoreV3
	after int
	reads int
	stop  func()
}

func (s *stopAfterReadsPlanStoreV3) GetAttempt(ctx context.Context, sessionID string) (*playback.AttemptRecordV3, error) {
	if s.reads == s.after {
		s.reads++
		s.stop()
	} else {
		s.reads++
	}
	return s.MemoryPlanStoreV3.GetAttempt(ctx, sessionID)
}

// TestRealtimeConfirmedStopNotifiesProvidersOnce covers a player-confirmed
// administrator stop: whether its queued worker finalizes the attempt, the
// client's stop or a terminate finalized it first, the client's stop holds
// the lease and finishes while the worker waits, or the worker finishes an
// abort whose server died, providers hear the end once and the admin log
// keeps the reason of the stop that won the row.
func TestRealtimeConfirmedStopNotifiesProvidersOnce(t *testing.T) {
	clientStop := func(t *testing.T, f *playbackServiceFixture, stopID string) {
		if _, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: stopID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		// position is the last progress before the stop; at 0 no watch
		// history row is written.
		position   float64
		wantReason PlaybackEndReason
		race       func(t *testing.T, f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3)
		// wantMeasured: the worker records the play from the attempt row,
		// with the measurement of the copy the confirmed stop removed.
		wantMeasured bool
	}{
		{name: "worker finalizes", position: 900, wantReason: PlaybackEndTerminated, wantMeasured: true},
		{name: "client stop finalized first", position: 900, wantReason: PlaybackEndStopped, race: func(t *testing.T, f *playbackServiceFixture, _ *playback.MemoryPlanStoreV3) {
			clientStop(t, f, uuid.NewString())
		}},
		{name: "worker finishes an abort whose server died", position: 900, wantReason: PlaybackEndAborted, race: func(t *testing.T, f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			if _, _, err := memory.StopAttempt(context.Background(), f.session.ID, abortStopID(f.session.ID), nil); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "terminate finalized first without a history row", wantReason: PlaybackEndTerminated, race: func(t *testing.T, f *playbackServiceFixture, _ *playback.MemoryPlanStoreV3) {
			if err := f.handler.endSessionAsAdmin(context.Background(), f.session.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "client stop finalizes while the worker waits", position: 900, wantReason: PlaybackEndStopped, race: func(t *testing.T, f *playbackServiceFixture, memory *playback.MemoryPlanStoreV3) {
			stopID := uuid.NewString()
			if _, _, err := memory.StopAttempt(context.Background(), f.session.ID, stopID, nil); err != nil {
				t.Fatal(err)
			}
			f.handler.PlanStoreV3 = &claimRacePlanStoreV3{MemoryPlanStoreV3: memory, race: func() { clientStop(t, f, stopID) }}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			scrobbler := &recordingPlaybackWatchScrobbler{}
			admin := &recordingPlaybackAdminStore{}
			f.handler.AdminStore = admin
			f.handler.WatchScrobbler = scrobbler
			f.handler.StoreProvider = testUserStoreProvider{store: newPlaybackTestStore(t)}
			f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
			if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: tc.position}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.manager.GetSession(f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.manager.StopSession(f.session.ID); err != nil {
				t.Fatal(err)
			}
			if tc.race != nil {
				tc.race(t, f, f.handler.PlanStoreV3.(*playback.MemoryPlanStoreV3))
			}
			f.handler.recordRealtimeStoppedSession(context.Background(), snapshot)
			if got := len(scrobbler.stops) + len(scrobbler.pauses); got != 1 {
				t.Fatalf("provider end events = %d stops + %d pauses, want 1", len(scrobbler.stops), len(scrobbler.pauses))
			}
			if len(admin.history) != 1 || admin.history[0].EndReason != tc.wantReason {
				t.Fatalf("admin history = %+v, want one %s row", admin.history, tc.wantReason)
			}
			if tc.wantMeasured && admin.history[0].Watch != snapshot.Watch {
				t.Fatalf("admin watch = %+v, want the removed copy's %+v", admin.history[0].Watch, snapshot.Watch)
			}
			if tc.wantReason == PlaybackEndAborted && (len(scrobbler.pauses) != 1 || !scrobbler.pauses[0].SessionEnded) {
				t.Fatalf("abort provider events = %+v, want one pause that ends the session", scrobbler.pauses)
			}
		})
	}
}

// claimRacePlanStoreV3 runs race just before the first finalization claim,
// as another finalizer that claims and finishes first would.
type claimRacePlanStoreV3 struct {
	*playback.MemoryPlanStoreV3
	race  func()
	raced bool
}

func (s *claimRacePlanStoreV3) ClaimStopFinalization(ctx context.Context, sessionID string, lease time.Time) (bool, error) {
	if !s.raced {
		s.raced = true
		s.race()
	}
	return s.MemoryPlanStoreV3.ClaimStopFinalization(ctx, sessionID, lease)
}

// TestPlaybackMutationsV2KeepAnotherAppToItsStreams: progress, stop, replan
// and route events check the account and profile, so a session another app opened,
// acting as the same profile, could move or end a stream the person's other
// device started once it knew the id. Such a caller is refused another login
// session's stream and one with no recorded login session, and acts on the
// stream its own session started without the rule being asked.
func TestPlaybackMutationsV2KeepAnotherAppToItsStreams(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	phone, err := f.manager.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := uuid.NewString()
	if err := f.handler.PlanStoreV3.SaveAttempt(context.Background(), playback.AttemptRecordV3{
		PlaybackAttemptID: attemptID, SessionID: phone.ID, UserID: 1, ProfileID: "profile-1",
		RequestedMediaFileID: 100, EffectiveMediaFileID: 100, ExpiresAt: time.Now().Add(time.Hour), LoginSessionID: "login-phone",
	}); err != nil {
		t.Fatal(err)
	}
	asked := 0
	app := f.caller
	app.LoginSessionID = "login-app"
	app.MayReachOtherDevices = func(context.Context) error {
		asked++
		return auth.ErrSessionNotAllowed
	}
	for _, session := range []string{phone.ID, f.session.ID} {
		_, err = f.handler.ApplyProgressV2(f.ctx, app, session, PlaybackProgressCommand{Sequence: 1, Position: 10})
		assertPlaybackOperationError(t, err, http.StatusForbidden, "forbidden")
		_, err = f.handler.StopPlaybackV2(f.ctx, app, session, PlaybackStopCommand{StopID: uuid.NewString()})
		assertPlaybackOperationError(t, err, http.StatusForbidden, "forbidden")
	}
	// A well-formed replan: with the rule passing it gets past validation and
	// answers 409 stale_playback_plan here (the fixture has no current plan),
	// so the 403 is the rule's.
	start := v3HandlerStartRequest()
	replan := PlaybackReplanCommand{Request: playback.ReplanRequestV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: attemptID, ReplanRequestID: "replan-0123456789", FailedPlanID: "plan-0123456789", PlanAttemptID: "plan-attempt-0123", PlanAttemptKey: "v3:0123456789abcdef", AttemptCount: 1, QualityPreference: "auto", Failure: playback.FailureV3{Classification: "network_error"}, Capabilities: start.Capabilities, ClientPlaybackContext: start.ClientPlaybackContext}, Digest: "d"}
	_, err = f.handler.ReplanPlaybackV2(f.ctx, app, phone.ID, replan)
	assertPlaybackOperationError(t, err, http.StatusForbidden, "forbidden")
	// A route event names the stream it reports on, so it is held to the
	// same rule.
	routeEvent := func() PlaybackRouteEventCommand {
		return PlaybackRouteEventCommand{EventID: uuid.NewString(), Event: playback.RouteEventV3{
			ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: attemptID, SessionID: phone.ID, Event: "first_frame",
			Diagnostics: map[string]string{"decoder_name": "synthetic"},
		}}
	}
	assertPlaybackOperationError(t, f.handler.ReportRouteEventV2(f.ctx, app, routeEvent()), http.StatusForbidden, "forbidden")

	own := app
	own.LoginSessionID = "login-phone"
	asked = 0
	if _, err := f.handler.ApplyProgressV2(f.ctx, own, phone.ID, PlaybackProgressCommand{Sequence: 1, Position: 10}); err != nil {
		t.Fatalf("progress on the caller's own stream = %v", err)
	}
	if err := f.handler.ReportRouteEventV2(f.ctx, own, routeEvent()); err != nil {
		t.Fatalf("route event on the caller's own stream = %v", err)
	}
	if asked != 0 {
		t.Fatalf("the rule was asked %d times for the caller's own stream", asked)
	}
}

// failingAttemptReadPlanStoreV3 is a plan store whose attempt read fails.
type failingAttemptReadPlanStoreV3 struct{ *playback.MemoryPlanStoreV3 }

func (failingAttemptReadPlanStoreV3) GetAttempt(context.Context, string) (*playback.AttemptRecordV3, error) {
	return nil, errors.New("store unreachable")
}

// TestReplanV2FailsClosedForAnotherApp: the replan reads the attempt to tell
// whose stream it is; when that read fails, a caller the rule applies to is
// refused as unavailable instead of being let through unchecked.
func TestReplanV2FailsClosedForAnotherApp(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	memory, ok := f.handler.PlanStoreV3.(*playback.MemoryPlanStoreV3)
	if !ok {
		t.Fatalf("plan store = %T", f.handler.PlanStoreV3)
	}
	f.handler.PlanStoreV3 = failingAttemptReadPlanStoreV3{memory}
	app := f.caller
	app.LoginSessionID = "login-app"
	app.MayReachOtherDevices = func(context.Context) error { return auth.ErrSessionNotAllowed }
	command := PlaybackReplanCommand{Request: playback.ReplanRequestV3{PlaybackAttemptID: uuid.NewString(), ReplanRequestID: uuid.NewString()}}
	_, err := f.handler.ReplanPlaybackV2(f.ctx, app, f.session.ID, command)
	assertPlaybackOperationError(t, err, http.StatusServiceUnavailable, "unavailable")
}

// countingSessions counts the session loads the media routes make: every
// load or rebuild of a playback session, and so every encode a load could
// restart, begins with GetSession. It wraps the in-memory session manager;
// no encoder runs in this test.
type countingSessions struct {
	SessionManagerInterface
	loads int
}

func (c *countingSessions) GetSession(id string) (*playback.Session, error) {
	c.loads++
	return c.SessionManagerInterface.GetSession(id)
}

// TestMediaRoutesKeepAnotherAppToItsStreams: the media routes check the
// account and profile only, so a session another app's key opened that held
// another device's playback session id could fetch that stream's bytes,
// manifest, segments, subtitles and fonts, and loading a live session whose
// encode died restarts it. Such a session is refused another login session's
// stream, and one with no recorded login session, before anything is loaded:
// the session manager is never asked, so no session is rebuilt and no encode
// started. Its own stream gets past the tie into the load and answers what
// the route answers when the fixture has no media behind it (404 for the
// file or transcode). The person's own session is never refused. Catches the
// tie going missing on a media route, or moving behind the load.
func TestMediaRoutesKeepAnotherAppToItsStreams(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 100, playback.PlayTranscode, false)
	if err != nil {
		t.Fatal(err)
	}
	// The original-file route serves a direct play.
	direct, err := manager.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &countingSessions{SessionManagerInterface: manager}
	store := playback.NewMemoryPlanStoreV3()
	handler := newKeyedPlaybackHandler(sessions)
	handler.PlanStoreV3 = store
	// A real file, so the original-file route serves the owning session.
	media := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(media, []byte("synthetic media bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	stream := NewStreamHandler(sessions, testPlaybackFileResolver{file: &models.MediaFile{ID: 100, FilePath: media}})
	stream.TM = handler.TranscodeManager()
	stream.PlanStoreV3 = store
	// answer runs one route as the caller and reports its status and code.
	answer := func(name, loginSession string, kind auth.SessionKind) (int, string) {
		t.Helper()
		id := session.ID
		if name == "stream" {
			id = direct.ID
		}
		req := playbackTestRequest(http.MethodGet, "/", nil, map[string]string{"session_id": id, "track": "0", "name": "segment0.m4s"})
		ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess, SessionID: loginSession, SessionKind: kind})
		ctx = apimw.SetProfileID(ctx, "profile-1")
		if name == "fonts" {
			_, err := stream.SubtitleFonts(ctx, SubtitleFontRequest{SessionID: session.ID, Track: "0", Query: url.Values{}})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				return http.StatusOK, ""
			}
			return apiErr.Status, apiErr.Code + ": " + apiErr.Message
		}
		rr := httptest.NewRecorder()
		map[string]http.HandlerFunc{
			"manifest": handler.HandleGetTranscodeManifest,
			"segment":  handler.HandleGetTranscodeSegment,
			"stream":   stream.HandleStream,
			"subtitle": stream.HandleSubtitle,
		}[name](rr, req.WithContext(ctx))
		var body struct{ Error, Message string }
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return rr.Code, body.Error + ": " + body.Message
	}
	refusal := "forbidden: " + SessionNotAllowedDetail
	// What the owning session gets past the tie: the original file is
	// served; the fixture has no transcode output and no subtitle track, so
	// those routes answer their own 404 once the session is loaded.
	ownStatus := map[string]int{"stream": http.StatusOK}
	ownAnswer := map[string]string{
		"manifest": "not_found: Transcode session not found",
		"segment":  "not_found: Transcode session not found",
		"stream":   ": ",
		"subtitle": "not_found: Subtitle track not found",
		"fonts":    "not_found: Embedded subtitle track not found",
	}
	routes := []string{"manifest", "segment", "stream", "subtitle", "fonts"}
	refused := func(name, why string) {
		t.Helper()
		before := sessions.loads
		if status, got := answer(name, "login-app", auth.SessionKindGrantedApp); status != http.StatusForbidden || got != refusal {
			t.Fatalf("%s, %s: %d %q", name, why, status, got)
		}
		if sessions.loads != before {
			t.Fatalf("%s, %s: the refused request loaded the session %d times", name, why, sessions.loads-before)
		}
	}
	for _, name := range routes {
		refused(name, "a stream with no recorded login session")
	}
	for _, id := range []string{session.ID, direct.ID} {
		if err := store.SaveAttempt(context.Background(), playback.AttemptRecordV3{
			PlaybackAttemptID: uuid.NewString(), SessionID: id, UserID: 1, ProfileID: "profile-1",
			RequestedMediaFileID: 100, EffectiveMediaFileID: 100, ExpiresAt: time.Now().Add(time.Hour), LoginSessionID: "login-phone",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range routes {
		refused(name, "another device's stream")
		for _, own := range []struct {
			who  string
			kind auth.SessionKind
		}{{"login-phone", auth.SessionKindGrantedApp}, {"login-tv", ""}} {
			before := sessions.loads
			want := cmp.Or(ownStatus[name], http.StatusNotFound)
			if status, got := answer(name, own.who, own.kind); status != want || got != ownAnswer[name] {
				t.Fatalf("%s as %s: %d %q, want %d %q", name, own.who, status, got, want, ownAnswer[name])
			}
			if sessions.loads == before {
				t.Fatalf("%s as %s: never reached the load", name, own.who)
			}
		}
	}
}
