package playback_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestSessionManager_CapturesStartNetwork(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)
	ctx := clientip.SetContext(t.Context(), "192.168.1.8")
	ctx = netaccess.WithPath(ctx, netaccess.Path{Provider: "tailscale"})
	session, err := sm.StartSessionWithContext(ctx, 1, "profile", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if session.ClientIP != "192.168.1.8" || session.RoutingNetworkProvider == nil || *session.RoutingNetworkProvider != "tailscale" || session.StreamLocation != "remote" {
		t.Fatalf("start network = (%q, %v)", session.ClientIP, session.RoutingNetworkProvider)
	}
	if err := sm.SetStreamLocation(session.ID, "local"); err != nil {
		t.Fatal(err)
	}
	if got, err := sm.GetSession(session.ID); err != nil || got.StreamLocation != "local" {
		t.Fatalf("frozen stream location = %v, %v", got, err)
	}
}

func TestSessionManagerOutputFormatFollowsReplacement(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)
	session := mustStartSession(t, sm, 1, "profile", 100, playback.PlayRemux, true)
	if err := sm.SetOutputFormat(session.ID, "fmp4", "hls"); err != nil {
		t.Fatal(err)
	}
	got, err := sm.GetSession(session.ID)
	if err != nil || got.OutputContainer != "fmp4" || got.OutputProtocol != "hls" {
		t.Fatal("transport output format was not recorded")
	}
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{PlayMethod: playback.PlayDirect, TranscodeRouteSet: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = sm.GetSession(session.ID)
	if got.OutputContainer != "" || got.OutputProtocol != "" {
		t.Fatal("a replacement retained the previous container")
	}
}

func TestSessionManager_StreamLimitEnforcement(t *testing.T) {
	sm := playback.NewSessionManager(2, 1) // max 2 streams, 1 transcode

	// Start two sessions (hits the limit).
	s1 := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)
	mustStartSession(t, sm, 1, "profile-1", 101, playback.PlayDirect, false)

	// Third session should fail.
	if _, err := sm.StartSession(1, "profile-1", 102, playback.PlayDirect, false); !errors.Is(err, playback.ErrTooManyStreams) {
		t.Errorf("StartSession 3 = %v, want ErrTooManyStreams", err)
	}

	// Stop one session and try again.
	if err := sm.StopSession(s1.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	mustStartSession(t, sm, 1, "profile-1", 102, playback.PlayDirect, false)
}

func TestSessionManager_TranscodeLimitEnforcement(t *testing.T) {
	sm := playback.NewSessionManager(5, 1) // max 5 streams, 1 transcode

	// Start one transcode session.
	s1 := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayTranscode, false)

	// Second transcode should fail.
	if _, err := sm.StartSession(1, "profile-1", 101, playback.PlayTranscode, false); !errors.Is(err, playback.ErrTooManyTranscodes) {
		t.Errorf("StartSession transcode 2 = %v, want ErrTooManyTranscodes", err)
	}

	// Direct play should still work (transcode limit doesn't block direct).
	mustStartSession(t, sm, 1, "profile-1", 102, playback.PlayDirect, false)

	// TranscodeCount should be 1.
	if sm.TranscodeCount(1) != 1 {
		t.Errorf("TranscodeCount = %d, want 1", sm.TranscodeCount(1))
	}

	// Stop the transcode session.
	if err := sm.StopSession(s1.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	// Now another transcode should work.
	mustStartSession(t, sm, 1, "profile-1", 103, playback.PlayTranscode, false)
}

func TestSessionManager_UserLimitProviderOverridesDefaults(t *testing.T) {
	sm := playback.NewSessionManager(6, 2)
	sm.SetLimitProvider(func(_ context.Context, userID int) (playback.SessionLimits, error) {
		switch userID {
		case 1:
			return playback.SessionLimits{MaxStreams: 1, MaxTranscodes: 1}, nil
		default:
			return playback.SessionLimits{MaxStreams: 6, MaxTranscodes: 2}, nil
		}
	})

	mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)
	if _, err := sm.StartSession(1, "profile-1", 101, playback.PlayDirect, false); !errors.Is(err, playback.ErrTooManyStreams) {
		t.Fatalf("StartSession user 1 over stream limit = %v, want ErrTooManyStreams", err)
	}

	mustStartSession(t, sm, 2, "profile-2", 200, playback.PlayDirect, false)
	mustStartSession(t, sm, 2, "profile-2", 201, playback.PlayDirect, false)
}

func TestSessionManager_DisabledVideoTranscodingAllowsAudioByDefault(t *testing.T) {
	sm := withFixedLimits(playback.NewSessionManager(0, 0), playback.SessionLimits{TranscodingDisabled: true})

	for _, tc := range []struct {
		name           string
		method         playback.PlayMethod
		transcodeAudio bool
		wantErr        error
	}{
		{name: "direct play", method: playback.PlayDirect},
		{name: "container remux", method: playback.PlayRemux},
		{name: "video transcode", method: playback.PlayTranscode, wantErr: playback.ErrTranscodingDisabled},
		{name: "audio transcode", method: playback.PlayRemux, transcodeAudio: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sm.StartSession(1, "profile-1", 100, tc.method, tc.transcodeAudio)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("StartSession() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestSessionManager_DisabledAudioTranscodingRejectsAudioTranscode(t *testing.T) {
	sm := withFixedLimits(playback.NewSessionManager(0, 0), playback.SessionLimits{TranscodingDisabled: true, AudioTranscodingDisabled: true})

	_, err := sm.StartSession(1, "profile-1", 100, playback.PlayRemux, true)
	if !errors.Is(err, playback.ErrAudioTranscodingDisabled) {
		t.Fatalf("StartSession() error = %v, want ErrAudioTranscodingDisabled", err)
	}
}

func TestSessionManager_CheckTranscodingAllowed(t *testing.T) {
	sm := withFixedLimits(playback.NewSessionManager(0, 0), playback.SessionLimits{TranscodingDisabled: true, AudioTranscodingDisabled: true})

	if err := sm.CheckTranscodingAllowed(context.Background(), 1, true); !errors.Is(err, playback.ErrTranscodingDisabled) {
		t.Fatalf("CheckTranscodingAllowed() error = %v, want ErrTranscodingDisabled", err)
	}
	if err := sm.CheckTranscodingAllowed(context.Background(), 1, false); !errors.Is(err, playback.ErrAudioTranscodingDisabled) {
		t.Fatalf("CheckTranscodingAllowed(audio) error = %v, want ErrAudioTranscodingDisabled", err)
	}
}

func TestSessionManager_PolicyAllowsAudioOnlyTranscodeWhenVideoTranscodingDisabled(t *testing.T) {
	sm := withFixedLimits(playback.NewSessionManager(0, 0), playback.SessionLimits{TranscodingDisabled: true})
	sm.SetAdmissionDecider(policy.NewPlaybackAdmissionDecider(newPlaybackPolicyPDP(t)))

	mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayRemux, true)
}

func TestSessionManager_PolicyAdmissionDeciderMatchesLegacy(t *testing.T) {
	pdp := newPlaybackPolicyPDP(t)
	ctx := context.Background()

	for _, maxStreams := range []int{0, 1, 2} {
		for _, maxTranscodes := range []int{0, 1, 2} {
			for _, activeStreams := range []int{0, 1, 2} {
				for _, activeTranscodes := range []int{0, 1, 2} {
					if activeTranscodes > activeStreams {
						continue
					}
					for _, method := range []playback.PlayMethod{playback.PlayDirect, playback.PlayTranscode} {
						name := fmt.Sprintf("streams_%d_transcodes_%d_active_%d_%d_method_%s",
							maxStreams, maxTranscodes, activeStreams, activeTranscodes, method)
						t.Run(name, func(t *testing.T) {
							limits := playback.SessionLimits{MaxStreams: maxStreams, MaxTranscodes: maxTranscodes}
							legacy := withFixedLimits(seededSessionManager(t, activeStreams, activeTranscodes), limits)
							withPolicy := withFixedLimits(seededSessionManager(t, activeStreams, activeTranscodes), limits)
							withPolicy.SetAdmissionDecider(policy.NewPlaybackAdmissionDecider(pdp))

							_, legacyErr := legacy.StartSessionWithContext(ctx, 1, "profile-1", 900, method, false)
							_, policyErr := withPolicy.StartSessionWithContext(ctx, 1, "profile-1", 900, method, false)
							if !sameAdmissionError(policyErr, legacyErr) {
								t.Fatalf("policy admission error = %v, want legacy %v", policyErr, legacyErr)
							}
						})
					}
				}
			}
		}
	}
}

func TestSessionManager_AdmissionDeciderErrorDenies(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)
	sm.SetAdmissionDecider(func(context.Context, playback.AdmissionRequest) (playback.AdmissionDecision, error) {
		return playback.AdmissionDecision{}, errors.New("policy unavailable")
	})

	_, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if !errors.Is(err, playback.ErrPlaybackNotAllowed) {
		t.Fatalf("StartSession with failing decider = %v, want ErrPlaybackNotAllowed", err)
	}
}

func TestSessionManager_AdmissionReasonCodesMapToSentinelErrors(t *testing.T) {
	cases := []struct {
		name       string
		reasonCode string
		want       error
	}{
		{"max streams", playback.AdmissionReasonMaxStreamsExceeded, playback.ErrTooManyStreams},
		{"max transcodes", playback.AdmissionReasonMaxTranscodesExceeded, playback.ErrTooManyTranscodes},
		{"transcoding disabled", playback.AdmissionReasonTranscodingDisabled, playback.ErrTranscodingDisabled},
		{"audio transcoding disabled", playback.AdmissionReasonAudioTranscodingDisabled, playback.ErrAudioTranscodingDisabled},
		// A custom-override denial carries free text and the custom_denial
		// code; it must not surface as a concurrency-limit error.
		{"custom denial", "custom_denial", playback.ErrPlaybackNotAllowed},
		{"empty code", "", playback.ErrPlaybackNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := playback.NewSessionManager(0, 0)
			sm.SetAdmissionDecider(func(context.Context, playback.AdmissionRequest) (playback.AdmissionDecision, error) {
				return playback.AdmissionDecision{Allowed: false, Reason: "quiet hours", ReasonCode: tc.reasonCode}, nil
			})
			_, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
			if !errors.Is(err, tc.want) {
				t.Fatalf("StartSession denial with code %q = %v, want %v", tc.reasonCode, err, tc.want)
			}
		})
	}
}

func seededSessionManager(t *testing.T, activeStreams, activeTranscodes int) *playback.SessionManager {
	t.Helper()
	sm := playback.NewSessionManager(0, 0)
	for i := 0; i < activeTranscodes; i++ {
		mustStartSession(t, sm, 1, "profile-1", 100+i, playback.PlayTranscode, false)
	}
	for i := activeTranscodes; i < activeStreams; i++ {
		mustStartSession(t, sm, 1, "profile-1", 100+i, playback.PlayDirect, false)
	}
	return sm
}

// withFixedLimits gives every user of sm the same limits.
func withFixedLimits(sm *playback.SessionManager, limits playback.SessionLimits) *playback.SessionManager {
	sm.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return limits, nil
	})
	return sm
}

func newPlaybackPolicyPDP(t *testing.T) *policy.PDP {
	t.Helper()
	engine, err := policy.NewEngine(context.Background())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	return policy.NewPDP(engine)
}

func sameAdmissionError(got, want error) bool {
	switch {
	case want == nil:
		return got == nil
	case errors.Is(want, playback.ErrTooManyStreams):
		return errors.Is(got, playback.ErrTooManyStreams)
	case errors.Is(want, playback.ErrTooManyTranscodes):
		return errors.Is(got, playback.ErrTooManyTranscodes)
	default:
		return errors.Is(got, want)
	}
}

func TestSessionManager_MultipleUsers(t *testing.T) {
	sm := playback.NewSessionManager(2, 1) // max 2 streams per user

	// User 1 fills their slots.
	mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)
	mustStartSession(t, sm, 1, "profile-1", 101, playback.PlayDirect, false)

	// User 1 should be blocked.
	if _, err := sm.StartSession(1, "profile-1", 102, playback.PlayDirect, false); !errors.Is(err, playback.ErrTooManyStreams) {
		t.Errorf("User1 session 3 = %v, want ErrTooManyStreams", err)
	}

	// User 2 should be unaffected.
	mustStartSession(t, sm, 2, "profile-2", 200, playback.PlayDirect, false)
	mustStartSession(t, sm, 2, "profile-2", 201, playback.PlayTranscode, false)

	// Verify counts are isolated.
	if sm.ActiveCount(1) != 2 {
		t.Errorf("User1 ActiveCount = %d, want 2", sm.ActiveCount(1))
	}
	if sm.ActiveCount(2) != 2 {
		t.Errorf("User2 ActiveCount = %d, want 2", sm.ActiveCount(2))
	}
	if sm.TranscodeCount(1) != 0 {
		t.Errorf("User1 TranscodeCount = %d, want 0", sm.TranscodeCount(1))
	}
	if sm.TranscodeCount(2) != 1 {
		t.Errorf("User2 TranscodeCount = %d, want 1", sm.TranscodeCount(2))
	}
}

func TestSessionManager_GetUserSessions(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	// Create sessions for two users.
	mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)
	mustStartSession(t, sm, 1, "profile-1", 101, playback.PlayTranscode, false)
	mustStartSession(t, sm, 2, "profile-2", 200, playback.PlayDirect, false)

	user1Sessions := sm.GetUserSessions(1)
	if len(user1Sessions) != 2 {
		t.Errorf("User1 sessions = %d, want 2", len(user1Sessions))
	}

	user2Sessions := sm.GetUserSessions(2)
	if len(user2Sessions) != 1 {
		t.Errorf("User2 sessions = %d, want 1", len(user2Sessions))
	}

	// Non-existent user should return nil/empty.
	user3Sessions := sm.GetUserSessions(3)
	if len(user3Sessions) != 0 {
		t.Errorf("User3 sessions = %d, want 0", len(user3Sessions))
	}
}

func TestSessionManager_AllSessions(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	// Empty manager returns empty slice.
	all := sm.AllSessions()
	if len(all) != 0 {
		t.Errorf("AllSessions on empty manager = %d, want 0", len(all))
	}

	// Start sessions for two users.
	s1 := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)
	s2 := mustStartSession(t, sm, 1, "profile-1", 101, playback.PlayRemux, false)
	s3 := mustStartSession(t, sm, 2, "profile-2", 200, playback.PlayTranscode, false)

	all = sm.AllSessions()
	if len(all) != 3 {
		t.Fatalf("AllSessions = %d, want 3", len(all))
	}

	// Verify sessions are returned (order is non-deterministic with maps).
	ids := map[string]bool{}
	for _, s := range all {
		ids[s.ID] = true
	}
	for _, expected := range []string{s1.ID, s2.ID, s3.ID} {
		if !ids[expected] {
			t.Errorf("AllSessions missing session %s", expected)
		}
	}

	// Verify returned sessions are copies (mutating shouldn't affect manager).
	all[0].Position = 999.0
	original, _ := sm.GetSession(all[0].ID)
	if original.Position == 999.0 {
		t.Error("AllSessions should return copies, but mutation propagated")
	}
}

func TestApplyReplacementIfRouteRejectsStalePredecessor(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	session := mustStartSession(t, mgr, 1, "profile-1", 42, playback.PlayRemux, true)
	if err := mgr.SetTranscodeRoute(session.ID, playback.TranscodeRoute{NodeURL: "http://old-node", TransportID: "old-transport"}); err != nil {
		t.Fatalf("SetTranscodeRoute: %v", err)
	}
	replacement := playback.SessionReplacement{
		EffectiveMediaFileID: 42,
		StreamState: playback.SessionStreamState{
			PlayMethod:           playback.PlayTranscode,
			BasePlayMethod:       playback.PlayRemux,
			TranscodeAudio:       true,
			TranscodeRouteSet:    true,
			SubtitleTrackIndex:   -1,
			TranscodeNodeURL:     "http://new-node",
			TranscodeTransportID: "new-transport",
		},
	}

	_, published, err := mgr.ApplyReplacementIfRoute(
		session.ID,
		playback.TranscodeRoute{NodeURL: "http://stale-node", TransportID: "stale-transport"},
		replacement,
	)
	if err != nil {
		t.Fatalf("ApplyReplacementIfRoute stale: %v", err)
	}
	if published {
		t.Fatal("stale predecessor unexpectedly published a replacement")
	}

	_, published, err = mgr.ApplyReplacementIfRoute(
		session.ID,
		playback.TranscodeRoute{NodeURL: "http://old-node", TransportID: "old-transport"},
		replacement,
	)
	if err != nil {
		t.Fatalf("ApplyReplacementIfRoute current: %v", err)
	}
	if !published {
		t.Fatal("current predecessor did not publish its replacement")
	}
	got := mustGetSession(t, mgr, session.ID)
	if got.TranscodeNodeURL != "http://new-node" || got.TranscodeTransportID != "new-transport" {
		t.Fatalf("transcode route = %q/%q", got.TranscodeNodeURL, got.TranscodeTransportID)
	}
}

// TestSessionReplacementAppliesAndRollsBackAtomically verifies failed replacement restores the prior stream.
func TestSessionReplacementAppliesAndRollsBackAtomically(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSessionWithFiles(7, "profile-1", 42, 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:             playback.PlayDirect,
		BasePlayMethod:         playback.PlayDirect,
		AudioTrackIndex:        0,
		TranscodeRouteSet:      true,
		SubtitleTrackIndex:     -1,
		StreamBitrateKbps:      8_000,
		SourceAudioChannels:    6,
		TranscodeHWAccel:       "qsv",
		ToneMapMode:            tonemap.ModeHardware,
		TranscodeNodeURL:       "http://old-node",
		TranscodeTransportID:   "old-transport",
		RoutingNetworkProvider: new("tailscale"),
	}); err != nil {
		t.Fatal(err)
	}
	position := 321.5
	rollback, err := manager.ApplyReplacement(session.ID, playback.SessionReplacement{
		EffectiveMediaFileID: 84,
		StreamState: playback.SessionStreamState{
			PlayMethod:             playback.PlayTranscode,
			BasePlayMethod:         playback.PlayTranscode,
			AudioTrackIndex:        2,
			TranscodeAudio:         true,
			TranscodeRouteSet:      true,
			SubtitleTrackIndex:     1,
			StreamBitrateKbps:      3_500,
			SourceAudioChannels:    8,
			TranscodeHWAccel:       "none",
			ToneMapMode:            tonemap.ModeSoftware,
			TranscodeNodeURL:       "http://new-node",
			TranscodeTransportID:   "new-transport",
			RoutingNetworkProvider: new(""),
		},
		PositionSeconds: &position,
		IsPaused:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced := mustGetSession(t, manager, session.ID)
	if replaced.MediaFileID != 84 || replaced.PlayMethod != playback.PlayTranscode || replaced.AudioTrackIndex != 2 ||
		replaced.SourceAudioChannels != 8 || replaced.RoutingNetworkProvider == nil || *replaced.RoutingNetworkProvider != "" ||
		replaced.TranscodeNodeURL != "http://new-node" || replaced.TranscodeHWAccel != "none" || replaced.ToneMapMode != "software" || replaced.Position != position || !replaced.IsPaused {
		t.Fatalf("replacement session = %#v", replaced)
	}
	if err := manager.RollbackReplacement(session.ID, rollback); err != nil {
		t.Fatal(err)
	}
	restored := mustGetSession(t, manager, session.ID)
	if restored.MediaFileID != 42 || restored.PlayMethod != playback.PlayDirect || restored.AudioTrackIndex != 0 ||
		restored.SourceAudioChannels != 6 || restored.RoutingNetworkProvider == nil || *restored.RoutingNetworkProvider != "tailscale" ||
		restored.TranscodeNodeURL != "http://old-node" || restored.TranscodeTransportID != "old-transport" ||
		restored.TranscodeHWAccel != "qsv" || restored.ToneMapMode != "hardware" ||
		restored.Position != 0 || restored.IsPaused {
		t.Fatalf("restored session = %#v", restored)
	}
}

func TestSessionReplacementRollbackRejectsNewerMutation(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session := mustStartSession(t, manager, 7, "profile-1", 42, playback.PlayDirect, false)
	rollback, err := manager.ApplyReplacement(session.ID, playback.SessionReplacement{
		EffectiveMediaFileID: 84,
		StreamState: playback.SessionStreamState{
			PlayMethod:         playback.PlayRemux,
			BasePlayMethod:     playback.PlayRemux,
			TranscodeRouteSet:  true,
			SubtitleTrackIndex: -1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.UpdateProgress(session.ID, 10, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.RollbackReplacement(session.ID, rollback); !errors.Is(err, playback.ErrSessionReplacementSuperseded) {
		t.Fatalf("rollback error = %v, want ErrSessionReplacementSuperseded", err)
	}
}

func TestUpdateStreamState(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	session := mustStartSession(t, mgr, 1, "profile-1", 100, playback.PlayRemux, false)

	if err := mgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:             playback.PlayTranscode,
		BasePlayMethod:         playback.PlayRemux,
		AudioTrackIndex:        2,
		TranscodeAudio:         true,
		ClientIP:               "10.0.0.10",
		StreamBitrateKbps:      4200,
		TargetResolution:       "1080p",
		TargetVideoCodec:       "h264",
		TargetAudioCodec:       "aac",
		SourceAudioChannels:    6,
		TargetAudioChannels:    2,
		TargetAudioBitrateKbps: 192,
		TargetBitrateKbps:      4000,
		TranscodeNodeURL:       "http://node-1",
		TranscodeTransportID:   "transport-1",
		TranscodeRouteSet:      true,
		SubtitleTrackIndex:     3,
		SubtitleBurnIn:         true,
		SegmentDuration:        4,
	}); err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}

	got := mustGetSession(t, mgr, session.ID)
	if got.PlayMethod != playback.PlayTranscode {
		t.Errorf("PlayMethod = %q, want %q", got.PlayMethod, playback.PlayTranscode)
	}
	if got.BasePlayMethod != playback.PlayRemux {
		t.Errorf("BasePlayMethod = %q, want %q", got.BasePlayMethod, playback.PlayRemux)
	}
	if got.AudioTrackIndex != 2 {
		t.Errorf("AudioTrackIndex = %d, want 2", got.AudioTrackIndex)
	}
	if got.TranscodeNodeURL != "http://node-1" || got.TranscodeTransportID != "transport-1" {
		t.Fatalf("transcode route = %q/%q", got.TranscodeNodeURL, got.TranscodeTransportID)
	}
	if !got.TranscodeAudio {
		t.Error("TranscodeAudio = false, want true")
	}
	if got.ClientIP != "10.0.0.10" {
		t.Errorf("ClientIP = %q, want %q", got.ClientIP, "10.0.0.10")
	}
	if got.StreamBitrateKbps != 4200 {
		t.Errorf("StreamBitrateKbps = %d, want 4200", got.StreamBitrateKbps)
	}
	if got.TargetResolution != "1080p" {
		t.Errorf("TargetResolution = %q, want %q", got.TargetResolution, "1080p")
	}
	if got.TargetVideoCodec != "h264" {
		t.Errorf("TargetVideoCodec = %q, want %q", got.TargetVideoCodec, "h264")
	}
	if got.TargetAudioCodec != "aac" {
		t.Errorf("TargetAudioCodec = %q, want %q", got.TargetAudioCodec, "aac")
	}
	if got.SourceAudioChannels != 6 || got.TargetAudioChannels != 2 {
		t.Errorf("audio channels = source %d / target %d, want 6 / 2", got.SourceAudioChannels, got.TargetAudioChannels)
	}
	if got.TargetBitrateKbps != 4000 {
		t.Errorf("TargetBitrateKbps = %d, want 4000", got.TargetBitrateKbps)
	}
	if got.SubtitleTrackIndex != 3 {
		t.Errorf("SubtitleTrackIndex = %d, want 3", got.SubtitleTrackIndex)
	}
	if !got.SubtitleBurnIn {
		t.Error("SubtitleBurnIn = false, want true")
	}
	if got.SegmentDuration != 4 {
		t.Errorf("SegmentDuration = %d, want 4", got.SegmentDuration)
	}
	if err := mgr.UpdateStreamState(session.ID, playback.SessionStreamState{AudioTrackIndex: 1}); err != nil {
		t.Fatal(err)
	}
	got = mustGetSession(t, mgr, session.ID)
	if !got.TranscodeAudio || got.TargetAudioCodec != "aac" || got.SourceAudioChannels != 6 ||
		got.TargetAudioChannels != 2 || got.TargetAudioBitrateKbps != 192 {
		t.Fatalf("audio recipe after partial update = transcode %t codec %q source %d target %d bitrate %d, want preserved AAC 6-to-2 at 192 kbps",
			got.TranscodeAudio, got.TargetAudioCodec, got.SourceAudioChannels, got.TargetAudioChannels, got.TargetAudioBitrateKbps)
	}
	if err := mgr.UpdateStreamState(session.ID, playback.SessionStreamState{TranscodeRouteSet: true}); err != nil {
		t.Fatal(err)
	}
	got = mustGetSession(t, mgr, session.ID)
	if got.TranscodeNodeURL != "" || got.TranscodeTransportID != "" {
		t.Fatalf("cleared transcode route = %q/%q", got.TranscodeNodeURL, got.TranscodeTransportID)
	}
	if got.TranscodeAudio || got.TargetAudioCodec != "" || got.SourceAudioChannels != 0 ||
		got.TargetAudioChannels != 0 || got.TargetAudioBitrateKbps != 0 {
		t.Fatalf("audio recipe after full route replacement = transcode %t codec %q source %d target %d bitrate %d, want cleared",
			got.TranscodeAudio, got.TargetAudioCodec, got.SourceAudioChannels, got.TargetAudioChannels, got.TargetAudioBitrateKbps)
	}
}

func TestUpdateAudioTrack_PreservesTranscodeTransportForRemuxBase(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	session := mustStartSession(t, mgr, 1, "profile-1", 100, playback.PlayRemux, false)

	if err := mgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:      playback.PlayTranscode,
		BasePlayMethod:  playback.PlayRemux,
		AudioTrackIndex: 0,
	}); err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}

	if err := mgr.UpdateAudioTrack(session.ID, 1, playback.PlayRemux); err != nil {
		t.Fatalf("UpdateAudioTrack: %v", err)
	}

	got := mustGetSession(t, mgr, session.ID)
	if got.PlayMethod != playback.PlayTranscode {
		t.Errorf("PlayMethod = %q, want %q", got.PlayMethod, playback.PlayTranscode)
	}
	if got.BasePlayMethod != playback.PlayRemux {
		t.Errorf("BasePlayMethod = %q, want %q", got.BasePlayMethod, playback.PlayRemux)
	}
	if got.AudioTrackIndex != 1 {
		t.Errorf("AudioTrackIndex = %d, want 1", got.AudioTrackIndex)
	}
}

func TestSetRealtimeConnection(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	session := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)

	got, _ := sm.GetSession(session.ID)
	if got.HasRealtimeConnection {
		t.Error("new session should not have realtime connection")
	}

	if err := sm.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection(true): %v", err)
	}

	got, _ = sm.GetSession(session.ID)
	if !got.HasRealtimeConnection {
		t.Error("HasRealtimeConnection should be true after SetRealtime(true)")
	}
	if !got.HasWebSocket {
		t.Error("HasWebSocket should mirror realtime connection state")
	}

	got.HasRealtimeConnection = false
	again := mustGetSession(t, sm, session.ID)
	if !again.HasRealtimeConnection {
		t.Error("mutating GetSession copy should not change manager state")
	}

	if err := sm.SetRealtimeConnection(session.ID, false); err != nil {
		t.Fatalf("SetRealtimeConnection(false): %v", err)
	}

	got = mustGetSession(t, sm, session.ID)
	if got.ID != session.ID {
		t.Fatalf("GetSession ID = %q, want %q", got.ID, session.ID)
	}
	if count := sm.ActiveCount(1); count != 1 {
		t.Errorf("ActiveCount after realtime disconnect = %d, want 1", count)
	}
	if got.HasRealtimeConnection {
		t.Error("HasRealtimeConnection should be false after SetRealtime(false)")
	}
	if got.HasWebSocket {
		t.Error("HasWebSocket should clear when realtime connection closes")
	}
}

func TestSessionManager_LimitCountsIgnoreStaleSessions(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)
	sm.SetLivenessGracePeriods(time.Hour, 2*time.Hour)

	direct := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)
	transcode := mustStartSession(t, sm, 1, "profile-1", 101, playback.PlayTranscode, false)

	stale := time.Now().Add(-2 * time.Hour)
	direct.LastActivityAt = stale
	direct.UpdatedAt = stale
	transcode.LastActivityAt = stale
	transcode.UpdatedAt = stale

	if got := sm.ActiveCount(1); got != 0 {
		t.Fatalf("ActiveCount after grace = %d, want 0", got)
	}
	if got := sm.TranscodeCount(1); got != 0 {
		t.Fatalf("TranscodeCount after grace = %d, want 0", got)
	}
}

func TestSessionManager_ActiveTransportKeepsSessionLive(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)
	sm.SetLivenessGracePeriods(time.Hour, 2*time.Hour)

	session := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)
	if err := sm.BeginTransport(session.ID); err != nil {
		t.Fatalf("BeginTransport: %v", err)
	}

	session.LastActivityAt = time.Now().Add(-2 * time.Hour)
	session.UpdatedAt = session.LastActivityAt

	if got := sm.ActiveCount(1); got != 1 {
		t.Fatalf("ActiveCount with active transport = %d, want 1", got)
	}

	if err := sm.EndTransport(session.ID); err != nil {
		t.Fatalf("EndTransport: %v", err)
	}
	session.LastActivityAt = time.Now().Add(-2 * time.Hour)
	session.UpdatedAt = session.LastActivityAt

	if got := sm.ActiveCount(1); got != 0 {
		t.Fatalf("ActiveCount after transport ends = %d, want 0", got)
	}
}

func TestSessionManager_ZeroLimits(t *testing.T) {
	// Zero limits means unlimited.
	sm := playback.NewSessionManager(0, 0)

	for i := range 10 {
		mustStartSession(t, sm, 1, "profile-1", i, playback.PlayTranscode, false)
	}

	if sm.ActiveCount(1) != 10 {
		t.Errorf("ActiveCount = %d, want 10", sm.ActiveCount(1))
	}
}

func TestSessionManager_CleanExpired(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)

	// Create three sessions.
	idle := mustStartSession(t, sm, 1, "prof", 100, playback.PlayDirect, false)
	active := mustStartSession(t, sm, 1, "prof", 101, playback.PlayDirect, false)
	transportActive := mustStartSession(t, sm, 1, "prof", 102, playback.PlayDirect, false)

	// Mark the third session as actively transporting media.
	_ = sm.BeginTransport(transportActive.ID)

	// Send a recent progress update on the "active" session so it stays fresh.
	_ = sm.UpdateProgress(active.ID, 42.0, false)

	// The "idle" session has no update since StartSession.
	// The "transportActive" session has an open media transport, so it should
	// be exempt.

	// CleanExpired with 0 duration expires everything without a WebSocket
	// that hasn't been updated "recently". Since all sessions were just
	// created, use a very short maxIdle to ensure only truly idle ones are
	// caught. We simulate staleness by using a large duration that none can
	// exceed.
	expired := sm.CleanExpired(0) // 0 means "expire anything older than now"

	// Both inactive sessions should be expired (UpdatedAt <= time.Now()).
	// The transport-active session should survive regardless of staleness.
	if len(expired) != 2 {
		t.Fatalf("CleanExpired(0) removed %d sessions, want 2", len(expired))
	}

	// Transport-active session should still exist.
	_, err := sm.GetSession(transportActive.ID)
	if err != nil {
		t.Errorf("transport-active session should survive CleanExpired, got: %v", err)
	}

	// The idle and active sessions should be gone.
	_, err = sm.GetSession(idle.ID)
	if !errors.Is(err, playback.ErrSessionNotFound) {
		t.Errorf("idle session should be expired, got: %v", err)
	}
	_, err = sm.GetSession(active.ID)
	if !errors.Is(err, playback.ErrSessionNotFound) {
		t.Errorf("active session should be expired with maxIdle=0, got: %v", err)
	}

	// Only 1 session should remain (the transport-active one).
	if sm.ActiveCount(1) != 1 {
		t.Errorf("ActiveCount = %d, want 1", sm.ActiveCount(1))
	}
}

func TestSessionManager_CleanInactive_TriggersAllExpirationHooks(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)

	session := mustStartSession(t, sm, 1, "prof", 100, playback.PlayDirect, false)

	called := make(chan string, 2)
	sm.SetExpirationHook(func(session *playback.Session) {
		called <- "native:" + session.ID
	})
	sm.AddExpirationHook(func(session *playback.Session) {
		called <- "compat:" + session.ID
	})

	if expired := sm.CleanInactive(0, 0); len(expired) != 1 {
		t.Fatalf("CleanInactive removed %d sessions, want 1", len(expired))
	}

	got := map[string]bool{}
	for range 2 {
		select {
		case call := <-called:
			got[call] = true
		case <-time.After(time.Second):
			t.Fatal("not all expiration hooks were called")
		}
	}
	for _, want := range []string{"native:" + session.ID, "compat:" + session.ID} {
		if !got[want] {
			t.Fatalf("expiration calls = %v, missing %q", got, want)
		}
	}
}

// A failed transcode session idle past the active grace is already absent from
// the live counts; replacement admission must exclude that session explicitly
// rather than decrement the totals, or the decrement frees a slot that belongs
// to another live transcode.
func TestCheckReplacementAllowedExcludesOnlyTheReplacedSession(t *testing.T) {
	sm := playback.NewSessionManager(10, 2)
	sm.SetLivenessGracePeriods(time.Hour, 2*time.Hour)

	failed := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayTranscode, false)
	// Age the failed session past the active grace so it no longer counts.
	failed.LastActivityAt = time.Now().Add(-2 * time.Hour)
	failed.UpdatedAt = failed.LastActivityAt

	for i := 0; i < 2; i++ {
		mustStartSession(t, sm, 1, "profile-1", 200+i, playback.PlayTranscode, false)
	}

	if err := sm.CheckReplacementAllowed(context.Background(), failed.ID, playback.PlayTranscode, false); !errors.Is(err, playback.ErrTooManyTranscodes) {
		t.Fatalf("CheckReplacementAllowed = %v, want ErrTooManyTranscodes (both slots are held by live sessions)", err)
	}
}

// Legacy stream updates (audio switches, progress-driven state) arriving while
// a v3 replacement is in flight must not release the capacity reservation;
// only the replacement's own route-set commit consumes it.
func TestLegacyStreamUpdateKeepsReplacementReservation(t *testing.T) {
	sm := playback.NewSessionManager(10, 1)

	session := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayDirect, false)
	if err := sm.CheckReplacementAllowed(context.Background(), session.ID, playback.PlayTranscode, false); err != nil {
		t.Fatalf("CheckReplacementAllowed: %v", err)
	}

	// A legacy partial update does not carry TranscodeRouteSet.
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{AudioTrackIndex: 1}); err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}

	if _, err := sm.StartSession(1, "profile-1", 200, playback.PlayTranscode, false); !errors.Is(err, playback.ErrTooManyTranscodes) {
		t.Fatalf("StartSession(transcode) = %v, want ErrTooManyTranscodes (reservation must survive the legacy update)", err)
	}

	// The route-set commit consumes the reservation and the slot converts to a
	// real transcode, so the cap stays enforced.
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{PlayMethod: playback.PlayTranscode, TranscodeRouteSet: true}); err != nil {
		t.Fatalf("UpdateStreamState(commit): %v", err)
	}
	if _, err := sm.StartSession(1, "profile-1", 201, playback.PlayTranscode, false); !errors.Is(err, playback.ErrTooManyTranscodes) {
		t.Fatalf("StartSession(transcode) after commit = %v, want ErrTooManyTranscodes", err)
	}
}

// A full v3 route description owns RemuxDVMode: a replan that lands on a
// non-DV source must clear a stale strip mode, while legacy partial updates
// must not clobber one.
func TestUpdateStreamStateClearsRemuxDVModeOnRouteSet(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	session := mustStartSession(t, sm, 1, "profile-1", 100, playback.PlayRemux, false)
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{RemuxDVMode: playback.RemuxDVStripToHDR10V3, TranscodeRouteSet: true}); err != nil {
		t.Fatalf("UpdateStreamState(set): %v", err)
	}

	// Legacy partial update: mode survives.
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{AudioTrackIndex: 1}); err != nil {
		t.Fatalf("UpdateStreamState(legacy): %v", err)
	}
	got := mustGetSession(t, sm, session.ID)
	if got.RemuxDVMode != playback.RemuxDVStripToHDR10V3 {
		t.Fatalf("RemuxDVMode after legacy update = %q, want strip_to_hdr10", got.RemuxDVMode)
	}

	// v3 route-set update for a non-DV source: mode clears.
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{TranscodeRouteSet: true}); err != nil {
		t.Fatalf("UpdateStreamState(clear): %v", err)
	}
	got = mustGetSession(t, sm, session.ID)
	if got.RemuxDVMode != "" {
		t.Fatalf("RemuxDVMode after route-set update = %q, want cleared", got.RemuxDVMode)
	}
}

func TestSessionManagerSetPausedReportsChanges(t *testing.T) {
	m := playback.NewSessionManager(0, 0)
	s := mustStartSession(t, m, 1, "profile-1", 100, playback.PlayDirect, false)
	if err := m.UpdateProgress(s.ID, 42, false); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.SetPaused(s.ID, true, 0, nil); err != nil || !changed {
		t.Fatalf("pause = %v, %v", changed, err)
	}
	if changed, err := m.SetPaused(s.ID, true, 0, nil); err != nil || changed {
		t.Fatalf("repeat pause = %v, %v", changed, err)
	}
	current, err := m.GetSession(s.ID)
	if err != nil || !current.IsPaused || current.Position != 42 {
		t.Fatalf("session = %+v, %v", current, err)
	}
	if _, err := m.SetPaused("missing", true, 0, nil); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("missing session err = %v", err)
	}
}

func TestSessionManagerConfirmedPauseFencesOnlyAcceptedSamples(t *testing.T) {
	m := playback.NewSessionManager(0, 0)
	s := mustStartSession(t, m, 1, "profile-1", 100, playback.PlayDirect, false)
	if _, err := m.SetPaused(s.ID, true, 1, nil); err != nil {
		t.Fatal(err)
	}
	// Even a confirmation of the same state advances the fence.
	if changed, err := m.SetPaused(s.ID, true, 2, nil); err != nil || changed {
		t.Fatalf("same-state confirmation = %v, %v", changed, err)
	}
	if err := m.UpdateProgressSample(s.ID, playback.ProgressSampleV3{Sequence: 2, Position: 42}); err != nil {
		t.Fatal(err)
	}
	current, err := m.GetSession(s.ID)
	if err != nil || !current.IsPaused || current.Position != 42 {
		t.Fatalf("accepted sample replay = %+v, %v", current, err)
	}
	if err := m.UpdateProgressSample(s.ID, playback.ProgressSampleV3{Sequence: 3, Position: 43}); err != nil {
		t.Fatal(err)
	}
	// An older/default sequence cannot weaken the earlier fence.
	if _, err := m.SetPaused(s.ID, false, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateProgressSample(s.ID, playback.ProgressSampleV3{Sequence: 2, Position: 44, IsPaused: true}); err != nil {
		t.Fatal(err)
	}
	current, err = m.GetSession(s.ID)
	if err != nil || current.IsPaused || current.Position != 44 {
		t.Fatalf("newer progress and retained fence = %+v, %v", current, err)
	}
}

// Progress side effects re-apply the latest accepted sample on retries;
// measuring it again would restart the watch-time interval and make the next
// report look like a seek.
func TestSessionManagerReappliedSampleLeavesWatchTime(t *testing.T) {
	m := playback.NewSessionManager(0, 0)
	s := mustStartSession(t, m, 1, "profile-1", 100, playback.PlayDirect, false)
	sample := playback.ProgressSampleV3{Sequence: 1, Position: 30}
	if err := m.UpdateProgressSample(s.ID, sample); err != nil {
		t.Fatal(err)
	}
	watch := mustGetSession(t, m, s.ID).Watch
	if err := m.UpdateProgressSample(s.ID, sample); err != nil {
		t.Fatal(err)
	}
	if got := mustGetSession(t, m, s.ID).Watch; got != watch {
		t.Fatalf("watch time after the sample was re-applied = %+v, want %+v", got, watch)
	}
}

// A confirmed pause carries no position, so the viewing between the last
// report and the pause is left for the next report to measure.
func TestSessionManagerConfirmedPauseKeepsViewingBeforeIt(t *testing.T) {
	m := playback.NewSessionManager(0, 0)
	s := mustStartSession(t, m, 1, "profile-1", 100, playback.PlayDirect, false)
	if err := m.UpdateProgressSample(s.ID, playback.ProgressSampleV3{Sequence: 1, Position: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetPaused(s.ID, true, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateProgressSample(s.ID, playback.ProgressSampleV3{Sequence: 2, Position: 101.5, IsPaused: true}); err != nil {
		t.Fatal(err)
	}
	if got := mustGetSession(t, m, s.ID).Watch.PlayedSeconds; got != 1.5 {
		t.Fatalf("played = %v, want 1.5", got)
	}
}

// A seek planned after accepted progress supersedes it: a confirmation that
// offers that same sample must not move the live position back.
func TestSessionManagerConfirmedPauseKeepsLaterSeekPosition(t *testing.T) {
	m := playback.NewSessionManager(0, 0)
	s := mustStartSession(t, m, 1, "profile-1", 100, playback.PlayDirect, false)
	if err := m.UpdateProgressSample(s.ID, playback.ProgressSampleV3{Sequence: 1, Position: 30}); err != nil {
		t.Fatal(err)
	}
	// Another replica accepted sequence 2; the seek was planned after it.
	seek := 321.0
	rollback, err := m.ApplyReplacement(s.ID, playback.SessionReplacement{EffectiveMediaFileID: 100, PositionSeconds: &seek, PositionSequence: 2, PreservePaused: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetPaused(s.ID, true, 2, &playback.ProgressSampleV3{Sequence: 2, Position: 31}); err != nil {
		t.Fatal(err)
	}
	current, err := m.GetSession(s.ID)
	if err != nil || !current.IsPaused || current.Position != 321 {
		t.Fatalf("after superseded sample = %+v, %v; want paused at seek position 321", current, err)
	}
	if err := m.RollbackReplacement(s.ID, rollback); !errors.Is(err, playback.ErrSessionReplacementSuperseded) {
		t.Fatalf("rollback after confirmation err = %v, want superseded", err)
	}
	if _, err := m.SetPaused(s.ID, false, 3, &playback.ProgressSampleV3{Sequence: 3, Position: 325}); err != nil {
		t.Fatal(err)
	}
	current, err = m.GetSession(s.ID)
	if err != nil || current.IsPaused || current.Position != 325 {
		t.Fatalf("after newer sample = %+v, %v; want playing at 325", current, err)
	}
}

func TestSessionManagerReplacementRollbackRestoresPositionSequence(t *testing.T) {
	m := playback.NewSessionManager(0, 0)
	s := mustStartSession(t, m, 1, "profile-1", 100, playback.PlayDirect, false)
	if err := m.UpdateProgressSample(s.ID, playback.ProgressSampleV3{Sequence: 1, Position: 30}); err != nil {
		t.Fatal(err)
	}
	watch := mustGetSession(t, m, s.ID).Watch
	seek := 321.0
	rollback, err := m.ApplyReplacement(s.ID, playback.SessionReplacement{EffectiveMediaFileID: 100, PositionSeconds: &seek, PositionSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RollbackReplacement(s.ID, rollback); err != nil {
		t.Fatal(err)
	}
	// The rolled-back seek must not stay in the watch-time measurement either.
	if got := mustGetSession(t, m, s.ID).Watch; got != watch {
		t.Fatalf("watch time after rollback = %+v, want %+v", got, watch)
	}
	// With the seek rolled back, sequence 2 is newer than the live position.
	if _, err := m.SetPaused(s.ID, true, 2, &playback.ProgressSampleV3{Sequence: 2, Position: 31}); err != nil {
		t.Fatal(err)
	}
	current, err := m.GetSession(s.ID)
	if err != nil || current.Position != 31 {
		t.Fatalf("after rollback = %+v, %v; want replica position 31", current, err)
	}
}

func mustStartSession(t *testing.T, sm *playback.SessionManager, userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) *playback.Session {
	t.Helper()
	session, err := sm.StartSession(userID, profileID, fileID, method, transcodeAudio)
	if err != nil {
		t.Fatalf("StartSession(user %d, file %d, %s): %v", userID, fileID, method, err)
	}
	return session
}

func mustGetSession(t *testing.T, sm *playback.SessionManager, sessionID string) *playback.Session {
	t.Helper()
	session, err := sm.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession(%s): %v", sessionID, err)
	}
	return session
}
