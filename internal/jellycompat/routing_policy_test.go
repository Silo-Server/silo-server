package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestCompatLocalHLSRouteAllowedHonorsHardPolicyBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		workload noderouting.Workload
		mutate   func(*config.PlaybackRoutingPolicy)
		want     bool
	}{
		{name: "remux default", workload: noderouting.WorkloadRemux, want: true},
		{name: "remux worker only", workload: noderouting.WorkloadRemux, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
		}},
		{name: "remux proxy only", workload: noderouting.WorkloadRemux, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.RemuxEgress = config.PlaybackEgressProxyOnly
		}},
		{name: "video default", workload: noderouting.WorkloadVideoTranscode, want: true},
		{name: "video worker only", workload: noderouting.WorkloadVideoTranscode, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
		}},
		{name: "video proxy only", workload: noderouting.WorkloadVideoTranscode, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.VideoTranscodeEgress = config.PlaybackEgressProxyOnly
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := config.DefaultPlaybackRoutingPolicy()
			if test.mutate != nil {
				test.mutate(&policy)
			}
			if got := compatLocalHLSRouteAllowed(test.workload, policy); got != test.want {
				t.Fatalf("compatLocalHLSRouteAllowed() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCompatWorkerHLSRouteAllowedHonorsHardExecutionBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		workload noderouting.Workload
		mutate   func(*config.PlaybackRoutingPolicy)
		want     bool
	}{
		{name: "remux default", workload: noderouting.WorkloadRemux, want: true},
		{name: "remux API only", workload: noderouting.WorkloadRemux, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.RemuxExecution = config.PlaybackExecutionAPIOnly
		}},
		{name: "video default", workload: noderouting.WorkloadVideoTranscode, want: true},
		{name: "video API only", workload: noderouting.WorkloadVideoTranscode, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.VideoTranscodeExecution = config.PlaybackExecutionAPIOnly
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := config.DefaultPlaybackRoutingPolicy()
			if test.mutate != nil {
				test.mutate(&policy)
			}
			if got := compatWorkerHLSRouteAllowed(test.workload, policy); got != test.want {
				t.Fatalf("compatWorkerHLSRouteAllowed() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCompatChildHLSRoutesRequireBoundPolicyCompliantTransport(t *testing.T) {
	tests := []struct {
		name       string
		segment    bool
		recipe     string
		assignment playback.NodeRoutingAssignment
		wantStatus int
		wantCode   string
	}{
		{name: "manifest rejects unbound route", wantStatus: http.StatusConflict, wantCode: compatPlaybackRouteUnboundCode},
		{name: "segment rejects unbound route", segment: true, wantStatus: http.StatusConflict, wantCode: compatPlaybackRouteUnboundCode},
		{
			name: "manifest rejects recipe outside committed workload", recipe: "local",
			assignment: playback.NodeRoutingAssignment{
				Workload: string(noderouting.WorkloadRemux), Execution: string(noderouting.ExecutionTranscode),
				Egress: string(noderouting.EgressAPI),
			},
			wantStatus: http.StatusServiceUnavailable, wantCode: compatRoutingPolicyUnsatisfiedCode,
		},
		{
			name: "manifest rejects API relay outside committed proxy route", recipe: "remote",
			assignment: playback.NodeRoutingAssignment{
				Workload: string(noderouting.WorkloadVideoTranscode), Execution: string(noderouting.ExecutionTranscode),
				ExecutionNodeURL: "http://worker-1", Egress: string(noderouting.EgressProxy),
			},
			wantStatus: http.StatusServiceUnavailable, wantCode: compatRoutingPolicyUnsatisfiedCode,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
			var recipe *playback.RecipeCard
			if test.recipe != "" {
				card := playback.NewRecipeCard(7, "profile-1", source.FileID, "", playback.TranscodeOpts{SessionID: "upstream-1"})
				if test.recipe == "remote" {
					card.TranscodeNodeURL = "http://worker-1"
				}
				recipe = &card
			}
			var assignment *playback.NodeRoutingAssignment
			if test.assignment.Workload != "" {
				copy := test.assignment
				assignment = &copy
			}
			store := NewPlaybackSessionStore(0, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1", UpstreamSessionID: "upstream-1",
				UpstreamPlayMethod: "transcode",
				MediaSources:       []PlaybackMediaSource{source}, Recipe: recipe, RoutingAssignment: assignment,
			})
			handler := &PlaybackHandler{playbackStore: store}

			request := httptest.NewRequest(http.MethodGet, "/Videos/item-1/hls/play-1/stream.m3u8", nil)
			routeCtx := chi.NewRouteContext()
			routeCtx.URLParams.Add("id", "item-1")
			routeCtx.URLParams.Add("playlistId", "play-1")
			if test.segment {
				routeCtx.URLParams.Add("segmentId", "0")
				routeCtx.URLParams.Add("segmentContainer", "ts")
			}
			ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
			ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7})
			recorder := httptest.NewRecorder()
			if test.segment {
				handler.HandleHLSSegment(recorder, request.WithContext(ctx))
			} else {
				handler.HandleHLSManifest(recorder, request.WithContext(ctx))
			}

			if recorder.Code != test.wantStatus || !strings.Contains(recorder.Body.String(), `"Error":"`+test.wantCode+`"`) {
				t.Fatalf("response = %d %s, want %d %s", recorder.Code, recorder.Body.String(), test.wantStatus, test.wantCode)
			}
		})
	}
}

func TestEnsureUpstreamPlaybackClearsStaleHLSRouteOnMethodSwitch(t *testing.T) {
	for _, method := range []string{"direct", "remux"} {
		t.Run(method, func(t *testing.T) {
			source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
			recipe := playback.NewRecipeCard(7, "profile-1", source.FileID, "", playback.TranscodeOpts{SessionID: "old-upstream"})
			assignment := playback.NodeRoutingAssignment{
				Workload: string(noderouting.WorkloadVideoTranscode), Execution: string(noderouting.ExecutionAPI),
				Egress: string(noderouting.EgressAPI),
			}
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1",
				UpstreamSessionID: "old-upstream", UpstreamPlayMethod: "transcode",
				MediaSources: []PlaybackMediaSource{source}, Recipe: &recipe, RoutingAssignment: &assignment,
			})
			manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
				"old-upstream": {ID: "old-upstream", UserID: 7, MediaFileID: source.FileID, PlayMethod: playback.PlayTranscode},
			}}
			handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}

			updated, err := handler.ensureUpstreamPlayback(
				t.Context(),
				&Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"},
				"play-1", source, method,
			)
			if err != nil {
				t.Fatalf("switch upstream method: %v", err)
			}
			if updated.UpstreamPlayMethod != method || updated.UpstreamSessionID == "old-upstream" {
				t.Fatalf("updated upstream = %q/%q, want fresh %s session", updated.UpstreamPlayMethod, updated.UpstreamSessionID, method)
			}
			if updated.Recipe != nil || updated.RoutingAssignment != nil {
				t.Fatalf("stale HLS route survived method switch: recipe=%#v assignment=%#v", updated.Recipe, updated.RoutingAssignment)
			}

			for _, endpoint := range []struct {
				name    string
				segment bool
			}{
				{name: "manifest"},
				{name: "segment", segment: true},
			} {
				t.Run(endpoint.name, func(t *testing.T) {
					request := httptest.NewRequest(http.MethodGet, "/Videos/item-1/hls/play-1/stream.m3u8", nil)
					routeCtx := chi.NewRouteContext()
					routeCtx.URLParams.Add("id", "item-1")
					routeCtx.URLParams.Add("playlistId", "play-1")
					if endpoint.segment {
						routeCtx.URLParams.Add("segmentId", "0")
						routeCtx.URLParams.Add("segmentContainer", "ts")
					}
					ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
					ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7})
					recorder := httptest.NewRecorder()
					if endpoint.segment {
						handler.HandleHLSSegment(recorder, request.WithContext(ctx))
					} else {
						handler.HandleHLSManifest(recorder, request.WithContext(ctx))
					}
					if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"Error":"`+compatPlaybackRouteUnboundCode+`"`) {
						t.Fatalf("child response = %d %s, want unbound-route conflict", recorder.Code, recorder.Body.String())
					}
					if manager.startCalls != 1 {
						t.Fatalf("upstream starts = %d, want only the method-switch start", manager.startCalls)
					}
				})
			}
		})
	}
}

// TestSourceSwitchReplacesTheUpstream covers a play that moves to another
// version of its item with the same play method: the upstream session bound
// to the first version's file must not serve the second.
func TestSourceSwitchReplacesTheUpstream(t *testing.T) {
	for _, method := range []string{"direct", "remux", "transcode"} {
		t.Run(method, func(t *testing.T) {
			codec := NewResourceIDCodec()
			first := testCompatSource(codec, testCompatVersion())
			secondVersion := testCompatVersion()
			secondVersion.FileID = 43
			second := testCompatSource(codec, secondVersion)
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1",
				UpstreamSessionID: "old-upstream", UpstreamPlayMethod: method, UpstreamMediaFileID: first.FileID,
				MediaSources: []PlaybackMediaSource{first, second},
			})
			manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
				"old-upstream": {ID: "old-upstream", UserID: 7, ProfileID: "profile-1", MediaFileID: first.FileID, PlayMethod: compatPlayMethod(method), Position: 300},
			}}
			handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}

			updated, err := handler.ensureUpstreamPlayback(t.Context(),
				&Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}, "play-1", second, method)
			if err != nil {
				t.Fatalf("switch source: %v", err)
			}
			if updated.UpstreamSessionID == "old-upstream" || updated.UpstreamMediaFileID != second.FileID {
				t.Fatalf("upstream = %q for file %d, want a fresh session for file %d",
					updated.UpstreamSessionID, updated.UpstreamMediaFileID, second.FileID)
			}
			if started := manager.sessions[updated.UpstreamSessionID]; started == nil || started.MediaFileID != second.FileID {
				t.Fatalf("started session = %+v, want one for file %d", started, second.FileID)
			}
			if !slices.Contains(manager.stopCalls, "old-upstream") {
				t.Fatalf("stopped sessions = %v, want the first version's session stopped", manager.stopCalls)
			}
			if len(manager.detached) != 1 || manager.detached[0].session.ID != "old-upstream" ||
				manager.detached[0].reason != playback.EndReasonReplaced {
				t.Fatalf("detached ends = %+v, want the first version's session recorded as replaced", manager.detached)
			}
		})
	}
}

// TestConcurrentVersionStartsAdoptOnlyTheSameFile covers two requests that
// start one play with different versions at once: the one that loses the
// attach may take over the winner's session only when it streams the version
// the loser was asked for.
func TestConcurrentVersionStartsAdoptOnlyTheSameFile(t *testing.T) {
	codec := NewResourceIDCodec()
	first := testCompatSource(codec, testCompatVersion())
	secondVersion := testCompatVersion()
	secondVersion.FileID = 43
	second := testCompatSource(codec, secondVersion)
	for _, tc := range []struct {
		name      string
		requested PlaybackMediaSource
		wantAdopt bool
	}{
		{name: "same version adopts the winner", requested: first, wantAdopt: true},
		{name: "other version refuses the winner", requested: second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1",
				MediaSources: []PlaybackMediaSource{first, second},
			})
			manager := &racingStartSessionManager{store: store, winnerMethod: "direct", winnerFileID: first.FileID, winnerLocal: true}
			handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}

			updated, err := handler.ensureUpstreamPlayback(t.Context(),
				&Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}, "play-1", tc.requested, "direct")
			if tc.wantAdopt {
				if err != nil || updated.UpstreamSessionID != "upstream-winner" {
					t.Fatalf("result = %+v, %v; want the winner adopted", updated, err)
				}
			} else if !errors.Is(err, errUpstreamReplaced) {
				t.Fatalf("result = %v, want errUpstreamReplaced instead of adopting %+v", err, updated)
			}
			if !slices.Contains(manager.stopCalls, "upstream-started") {
				t.Fatalf("stopped sessions = %v, want the losing start stopped", manager.stopCalls)
			}
		})
	}
}

// replaceAfterAttachStore lets a concurrent switch replace the upstream
// session right after this request attaches its own.
type replaceAfterAttachStore struct {
	CompatPlaybackStore
	attached string
	replace  func()
}

func (s *replaceAfterAttachStore) Update(id string, mutate func(*PlaybackSession) error) error {
	err := s.CompatPlaybackStore.Update(id, mutate)
	if current, ok := s.Get(id); err == nil && ok && current.UpstreamSessionID == s.attached && s.replace != nil {
		replace := s.replace
		s.replace = nil
		replace()
	}
	return err
}

// TestAttachedUpstreamReplacedBeforeServing covers a request whose attach
// succeeds but that a switch to another version replaces before it serves:
// it must not go on under the session that now streams the other version.
func TestAttachedUpstreamReplacedBeforeServing(t *testing.T) {
	codec := NewResourceIDCodec()
	first := testCompatSource(codec, testCompatVersion())
	secondVersion := testCompatVersion()
	secondVersion.FileID = 43
	second := testCompatSource(codec, secondVersion)
	inner := NewPlaybackSessionStore(time.Hour, nil)
	inner.Put(PlaybackSession{
		ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1",
		MediaSources: []PlaybackMediaSource{first, second},
	})
	store := &replaceAfterAttachStore{CompatPlaybackStore: inner, attached: "upstream-started"}
	store.replace = func() {
		_ = inner.Update("play-1", func(current *PlaybackSession) error {
			current.UpstreamSessionID, current.UpstreamMediaFileID = "upstream-second", second.FileID
			return nil
		})
	}
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{}}
	handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}

	updated, err := handler.ensureUpstreamPlayback(t.Context(),
		&Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}, "play-1", first, "direct")
	if !errors.Is(err, errUpstreamReplaced) {
		t.Fatalf("result = %v, want errUpstreamReplaced instead of serving under %+v", err, updated)
	}
	if !slices.Contains(manager.stopCalls, "upstream-started") {
		t.Fatalf("stopped sessions = %v, want this request's session stopped", manager.stopCalls)
	}
}

// TestChildHLSRequestForAnotherVersionKeepsThePlay covers a late child
// playlist for the version a play switched away from: only the master
// manifest switches versions, so the request is refused and the version
// being watched keeps its session.
func TestChildHLSRequestForAnotherVersionKeepsThePlay(t *testing.T) {
	codec := NewResourceIDCodec()
	first := testCompatSource(codec, testCompatVersion())
	secondVersion := testCompatVersion()
	secondVersion.FileID = 43
	second := testCompatSource(codec, secondVersion)
	recipe := playback.NewRecipeCard(7, "profile-1", second.FileID, "", playback.TranscodeOpts{SessionID: "upstream-second"})
	assignment := playback.NodeRoutingAssignment{
		Workload: string(noderouting.WorkloadVideoTranscode), Execution: string(noderouting.ExecutionAPI),
		Egress: string(noderouting.EgressAPI),
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1",
		UpstreamSessionID: "upstream-second", UpstreamPlayMethod: "transcode", UpstreamMediaFileID: second.FileID,
		MediaSources: []PlaybackMediaSource{first, second}, Recipe: &recipe, RoutingAssignment: &assignment,
	})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-second": {ID: "upstream-second", UserID: 7, MediaFileID: second.FileID, PlayMethod: playback.PlayTranscode},
	}}
	handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}

	request := httptest.NewRequest(http.MethodGet, "/Videos/item-1/hls/play-1/stream.m3u8?MediaSourceId="+first.ID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "item-1")
	routeCtx.URLParams.Add("playlistId", "play-1")
	ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7})
	recorder := httptest.NewRecorder()
	handler.HandleHLSManifest(recorder, request.WithContext(ctx))

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"Error":"`+compatRoutingPolicyUnsatisfiedCode+`"`) {
		t.Fatalf("child response = %d %s, want the obsolete-route refusal", recorder.Code, recorder.Body.String())
	}
	if len(manager.stopCalls) != 0 || manager.startCalls != 0 {
		t.Fatalf("stops = %v, starts = %d; want the watched version's session left alone", manager.stopCalls, manager.startCalls)
	}
}

// TestBoundRequestsNeverReplaceTheUpstream covers requests that follow an
// opened stream (a child playlist, a report's revive) racing a switch: one
// that names the play method or version the play left is late, and must not
// switch the play back.
func TestBoundRequestsNeverReplaceTheUpstream(t *testing.T) {
	codec := NewResourceIDCodec()
	first := testCompatSource(codec, testCompatVersion())
	secondVersion := testCompatVersion()
	secondVersion.FileID = 43
	second := testCompatSource(codec, secondVersion)
	compatSession := &Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}
	// The HLS playlists, master and child, ensure the upstream through the
	// transcode manifest; a report's revive ensures it directly.
	viaManifest := func(h *PlaybackHandler, source PlaybackMediaSource, _ string) error {
		_, err := h.ensureTranscodeManifestWithToneMapMode(t.Context(), compatSession, "play-1", source, "")
		return err
	}
	direct := func(h *PlaybackHandler, source PlaybackMediaSource, method string) error {
		_, err := h.ensureBoundUpstreamPlayback(t.Context(), compatSession, "play-1", source, method)
		return err
	}
	for _, tc := range []struct {
		name       string
		ensure     func(*PlaybackHandler, PlaybackMediaSource, string) error
		source     PlaybackMediaSource
		method     string
		reaped     bool
		wantStarts int
	}{
		{name: "playlist for the version the play left", ensure: viaManifest, source: first, method: "transcode"},
		{name: "revive for the method the play left", ensure: direct, source: second, method: "remux"},
		// A reaped upstream that can't be rebuilt is not a switch: the play
		// goes on under a fresh session.
		{name: "reaped upstream of the same version", ensure: direct, source: second, method: "transcode", reaped: true, wantStarts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1",
				UpstreamSessionID: "upstream-second", UpstreamPlayMethod: "transcode", UpstreamMediaFileID: second.FileID,
				MediaSources: []PlaybackMediaSource{first, second},
			})
			manager := &testCompatSessionManager{sessions: map[string]*playback.Session{}}
			if !tc.reaped {
				manager.sessions["upstream-second"] = &playback.Session{ID: "upstream-second", UserID: 7, MediaFileID: second.FileID, PlayMethod: playback.PlayTranscode}
			}
			handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}

			err := tc.ensure(handler, tc.source, tc.method)
			if tc.wantStarts == 0 && !errors.Is(err, errUpstreamReplaced) {
				t.Fatalf("err = %v, want errUpstreamReplaced", err)
			}
			if tc.wantStarts > 0 && err != nil {
				t.Fatalf("err = %v, want the play resumed", err)
			}
			if len(manager.stopCalls) != 0 || manager.startCalls != tc.wantStarts {
				t.Fatalf("stops = %v, starts = %d; want no stop and %d starts", manager.stopCalls, manager.startCalls, tc.wantStarts)
			}
		})
	}
}

// TestChildHLSSourceFollowsTheStreamedVersion covers child HLS requests
// without a MediaSourceId on a play that lists several versions.
func TestChildHLSSourceFollowsTheStreamedVersion(t *testing.T) {
	codec := NewResourceIDCodec()
	first := testCompatSource(codec, testCompatVersion())
	secondVersion := testCompatVersion()
	secondVersion.FileID = 43
	second := testCompatSource(codec, secondVersion)
	for _, tc := range []struct {
		name        string
		boundFileID int
		wantFileID  int
	}{
		{name: "bound version without an id", boundFileID: second.FileID, wantFileID: second.FileID},
		{name: "unrecorded file without an id", wantFileID: first.FileID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			play := &PlaybackSession{UpstreamMediaFileID: tc.boundFileID, MediaSources: []PlaybackMediaSource{first, second}}
			if got := compatChildHLSSource(play, ""); got == nil || got.FileID != tc.wantFileID {
				t.Fatalf("source = %+v, want file %d", got, tc.wantFileID)
			}
			// A progressive request that names no version must not switch
			// the play back to the first one listed.
			if got := playbackRouteSource(play, "", true, false); got == nil || got.FileID != tc.wantFileID {
				t.Fatalf("progressive source = %+v, want file %d", got, tc.wantFileID)
			}
		})
	}
}

// TestMethodSwitchEndsTheReplacedSession covers the session a Jellyfin
// method switch leaves behind: whether this replica held its copy or not,
// the switch records it as replaced once the play has moved to the new
// session, and a copy held elsewhere is recorded from its identity.
func TestMethodSwitchEndsTheReplacedSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		held       bool
		startFails bool
	}{
		{name: "copy held here", held: true},
		{name: "copy held elsewhere"},
		{name: "replacement fails to start", held: true, startFails: true},
	} {
		held := tc.held
		t.Run(tc.name, func(t *testing.T) {
			source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1",
				UpstreamSessionID: "old-upstream", UpstreamPlayMethod: "transcode", UpstreamMediaFileID: source.FileID,
				MediaSources: []PlaybackMediaSource{source},
			})
			manager := &testCompatSessionManager{sessions: map[string]*playback.Session{}}
			if tc.startFails {
				manager.startErr = errors.New("start failed")
			}
			if held {
				manager.sessions["old-upstream"] = &playback.Session{ID: "old-upstream", UserID: 7, ProfileID: "profile-1", MediaFileID: source.FileID, PlayMethod: playback.PlayTranscode, Position: 600}
			}
			handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}
			updated, err := handler.ensureUpstreamPlayback(t.Context(),
				&Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}, "play-1", source, "direct")
			if tc.startFails != (err != nil) || (err == nil && updated.UpstreamSessionID == "old-upstream") {
				t.Fatalf("switch = %+v, %v", updated, err)
			}
			if len(manager.detached) != 1 {
				t.Fatalf("detached ends = %+v, want the replaced session once", manager.detached)
			}
			end := manager.detached[0]
			wantPosition := float64(playback.PositionUnknown)
			if held {
				wantPosition = 600
			}
			if end.reason != playback.EndReasonReplaced || end.session.ID != "old-upstream" || end.session.UserID != 7 ||
				!end.session.IsJellyfinCompat || end.session.Position != wantPosition {
				t.Fatalf("replaced end = %+v reason %q", end.session, end.reason)
			}
		})
	}
}
