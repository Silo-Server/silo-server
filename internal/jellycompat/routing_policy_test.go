package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
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

func TestBuildPlaybackSourceHonorsProgressiveRemuxRoutingPolicy(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 1, Container: "mkv", CodecVideo: "h264", CodecAudio: "aac",
		VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Default: true}},
	}
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type: "Video", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
		}},
	}

	tests := []struct {
		name             string
		execution        config.PlaybackExecutionPreference
		egress           config.PlaybackEgressPreference
		wantDirectStream bool
	}{
		{
			name:      "API execution with API egress keeps progressive remux",
			execution: config.PlaybackExecutionAPIOnly, egress: config.PlaybackEgressAPIOnly,
			wantDirectStream: true,
		},
		{
			name:      "transcode preference does not rewrite the requested Jellyfin transport",
			execution: config.PlaybackExecutionPreferTranscode, egress: config.PlaybackEgressPreferProxy,
			wantDirectStream: true,
		},
		{
			name:      "worker execution with API egress falls back to HLS",
			execution: config.PlaybackExecutionWorkerOnly, egress: config.PlaybackEgressAPIOnly,
			wantDirectStream: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := config.DefaultPlaybackRoutingPolicy()
			policy.RemuxExecution = test.execution
			policy.RemuxEgress = test.egress
			handler := &PlaybackHandler{
				codec:     NewResourceIDCodec(),
				JWTSecret: "test-secret",
				PlaybackConfig: func() config.PlaybackConfig {
					return config.PlaybackConfig{Routing: policy}
				},
			}

			source := handler.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, true)
			if source.SupportsDirectPlay {
				t.Fatal("SupportsDirectPlay = true, want false for MKV source and MP4 profile")
			}
			if source.SupportsDirectStream != test.wantDirectStream {
				t.Fatalf("SupportsDirectStream = %v, want %v", source.SupportsDirectStream, test.wantDirectStream)
			}
			if !source.SupportsTranscoding {
				t.Fatal("SupportsTranscoding = false, want negotiated HLS fallback")
			}

			dto := handler.mediaSourceDTO("item", "play", "token", source)
			if (dto.DirectStreamURL != "") != test.wantDirectStream {
				t.Fatalf("DirectStreamURL = %q, advertised direct stream = %v", dto.DirectStreamURL, test.wantDirectStream)
			}
			if dto.TranscodingURL == "" {
				t.Fatal("TranscodingURL is empty, want HLS fallback")
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
			name: "segment rejects recipe outside committed workload", segment: true, recipe: "local",
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
		{
			name: "segment rejects API relay outside committed proxy route", segment: true, recipe: "remote",
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
				MediaSources: []PlaybackMediaSource{source}, Recipe: recipe, RoutingAssignment: assignment,
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

func TestRecordNodeRoutingAssignmentPersistsChildRouteBinding(t *testing.T) {
	store := NewPlaybackSessionStore(0, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "compat-token"})
	handler := &PlaybackHandler{playbackStore: store}
	want := playback.NodeRoutingAssignment{
		Workload: string(noderouting.WorkloadVideoTranscode), Execution: string(noderouting.ExecutionTranscode),
		ExecutionNodeID: 2, ExecutionNodeURL: "http://worker-1",
		Egress: string(noderouting.EgressAPI),
	}

	if err := handler.recordNodeRoutingAssignment(t.Context(), "play-1", "upstream-1", want); err != nil {
		t.Fatalf("record route: %v", err)
	}
	stored, ok := store.Get("play-1")
	if !ok || stored.RoutingAssignment == nil || *stored.RoutingAssignment != want {
		t.Fatalf("stored route = %#v, want %#v", stored.RoutingAssignment, want)
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
