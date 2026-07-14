package playback_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/policy"
)

func TestSessionManager_StartStop(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if session.ID == "" {
		t.Error("session ID is empty")
	}
	if session.UserID != 1 {
		t.Errorf("UserID = %d, want 1", session.UserID)
	}
	if session.ProfileID != "profile-1" {
		t.Errorf("ProfileID = %q, want profile-1", session.ProfileID)
	}
	if session.MediaFileID != 100 {
		t.Errorf("MediaFileID = %d, want 100", session.MediaFileID)
	}
	if session.RequestedMediaFileID != 100 {
		t.Errorf("RequestedMediaFileID = %d, want 100", session.RequestedMediaFileID)
	}
	if session.PlayMethod != playback.PlayDirect {
		t.Errorf("PlayMethod = %q, want direct", session.PlayMethod)
	}
	if session.BasePlayMethod != playback.PlayDirect {
		t.Errorf("BasePlayMethod = %q, want direct", session.BasePlayMethod)
	}
	if session.IsPaused {
		t.Error("new session should not be paused")
	}

	// GetSession should return the session.
	got, err := sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != session.ID {
		t.Errorf("GetSession ID = %q, want %q", got.ID, session.ID)
	}

	// ActiveCount should be 1.
	if sm.ActiveCount(1) != 1 {
		t.Errorf("ActiveCount = %d, want 1", sm.ActiveCount(1))
	}

	// Stop the session.
	if err := sm.StopSession(session.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	// GetSession should now fail.
	_, err = sm.GetSession(session.ID)
	if err != playback.ErrSessionNotFound {
		t.Errorf("GetSession after stop = %v, want ErrSessionNotFound", err)
	}

	// ActiveCount should be 0.
	if sm.ActiveCount(1) != 0 {
		t.Errorf("ActiveCount after stop = %d, want 0", sm.ActiveCount(1))
	}
}

func TestSessionManager_StopNonExistent(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	err := sm.StopSession("nonexistent-id")
	if err != playback.ErrSessionNotFound {
		t.Errorf("StopSession(nonexistent) = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionManager_UpdateProgress(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Update progress.
	if err := sm.UpdateProgress(session.ID, 123.5, false); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}

	got, err := sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Position != 123.5 {
		t.Errorf("Position = %f, want 123.5", got.Position)
	}
	if got.IsPaused {
		t.Error("IsPaused should be false")
	}

	// Pause.
	if err := sm.UpdateProgress(session.ID, 123.5, true); err != nil {
		t.Fatalf("UpdateProgress(pause): %v", err)
	}

	got, err = sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !got.IsPaused {
		t.Error("IsPaused should be true after pause")
	}
}

func TestSessionManager_UpdateProgress_NotFound(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	err := sm.UpdateProgress("nonexistent", 0, false)
	if err != playback.ErrSessionNotFound {
		t.Errorf("UpdateProgress(nonexistent) = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionManager_GetSessionsByMediaFileID(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	matchA, _ := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	matchB, _ := sm.StartSession(2, "profile-2", 100, playback.PlayDirect, false)
	matchRequested, _ := sm.StartSessionWithFiles(4, "profile-4", 200, 100, playback.PlayDirect, false)
	other, _ := sm.StartSession(3, "profile-3", 101, playback.PlayDirect, false)

	sessions := sm.GetSessionsByMediaFileID(100)
	if len(sessions) != 3 {
		t.Fatalf("len(GetSessionsByMediaFileID(100)) = %d, want 3", len(sessions))
	}

	gotIDs := map[string]struct{}{}
	for _, session := range sessions {
		gotIDs[session.ID] = struct{}{}
	}
	if _, ok := gotIDs[matchA.ID]; !ok {
		t.Fatalf("missing matching session %q", matchA.ID)
	}
	if _, ok := gotIDs[matchB.ID]; !ok {
		t.Fatalf("missing matching session %q", matchB.ID)
	}
	if _, ok := gotIDs[matchRequested.ID]; !ok {
		t.Fatalf("missing requested-file session %q", matchRequested.ID)
	}
	if _, ok := gotIDs[other.ID]; ok {
		t.Fatalf("unexpected non-matching session %q", other.ID)
	}
}

func TestUpdateAudioTrack(t *testing.T) {
	sm := playback.NewSessionManager(10, 5)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Switch to audio track 2 with remux method.
	if err := sm.UpdateAudioTrack(session.ID, 2, playback.PlayRemux); err != nil {
		t.Fatalf("UpdateAudioTrack: %v", err)
	}

	got, err := sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.AudioTrackIndex != 2 {
		t.Errorf("AudioTrackIndex = %d, want 2", got.AudioTrackIndex)
	}
	if got.PlayMethod != playback.PlayRemux {
		t.Errorf("PlayMethod = %q, want %q", got.PlayMethod, playback.PlayRemux)
	}
	if got.BasePlayMethod != playback.PlayRemux {
		t.Errorf("BasePlayMethod = %q, want %q", got.BasePlayMethod, playback.PlayRemux)
	}

	// Nonexistent session should return ErrSessionNotFound.
	err = sm.UpdateAudioTrack("nonexistent-id", 1, playback.PlayDirect)
	if err != playback.ErrSessionNotFound {
		t.Errorf("UpdateAudioTrack(nonexistent) = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionManager_StreamLimitEnforcement(t *testing.T) {
	sm := playback.NewSessionManager(2, 1) // max 2 streams, 1 transcode

	// Start two sessions (hits the limit).
	s1, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession 1: %v", err)
	}
	_, err = sm.StartSession(1, "profile-1", 101, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession 2: %v", err)
	}

	// Third session should fail.
	_, err = sm.StartSession(1, "profile-1", 102, playback.PlayDirect, false)
	if err != playback.ErrTooManyStreams {
		t.Errorf("StartSession 3 = %v, want ErrTooManyStreams", err)
	}

	// Stop one session and try again.
	if err := sm.StopSession(s1.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	_, err = sm.StartSession(1, "profile-1", 102, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession after stop: %v", err)
	}
}

func TestSessionManager_TranscodeLimitEnforcement(t *testing.T) {
	sm := playback.NewSessionManager(5, 1) // max 5 streams, 1 transcode

	// Start one transcode session.
	s1, err := sm.StartSession(1, "profile-1", 100, playback.PlayTranscode, false)
	if err != nil {
		t.Fatalf("StartSession transcode 1: %v", err)
	}

	// Second transcode should fail.
	_, err = sm.StartSession(1, "profile-1", 101, playback.PlayTranscode, false)
	if err != playback.ErrTooManyTranscodes {
		t.Errorf("StartSession transcode 2 = %v, want ErrTooManyTranscodes", err)
	}

	// Direct play should still work (transcode limit doesn't block direct).
	_, err = sm.StartSession(1, "profile-1", 102, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession direct while transcode limit hit: %v", err)
	}

	// TranscodeCount should be 1.
	if sm.TranscodeCount(1) != 1 {
		t.Errorf("TranscodeCount = %d, want 1", sm.TranscodeCount(1))
	}

	// Stop the transcode session.
	if err := sm.StopSession(s1.ID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	// Now another transcode should work.
	_, err = sm.StartSession(1, "profile-1", 103, playback.PlayTranscode, false)
	if err != nil {
		t.Fatalf("StartSession transcode after stop: %v", err)
	}
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

	if _, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false); err != nil {
		t.Fatalf("StartSession user 1: %v", err)
	}
	if _, err := sm.StartSession(1, "profile-1", 101, playback.PlayDirect, false); err != playback.ErrTooManyStreams {
		t.Fatalf("StartSession user 1 over stream limit = %v, want ErrTooManyStreams", err)
	}

	if _, err := sm.StartSession(2, "profile-2", 200, playback.PlayDirect, false); err != nil {
		t.Fatalf("StartSession user 2: %v", err)
	}
	if _, err := sm.StartSession(2, "profile-2", 201, playback.PlayDirect, false); err != nil {
		t.Fatalf("StartSession user 2 second stream: %v", err)
	}
}

func TestSessionManager_GroupPolicyLimitUsesStricterValue(t *testing.T) {
	user := &models.User{ID: 1, MaxStreams: 6, MaxTranscodes: 2}
	group := &access.GroupPolicy{MaxStreams: 1, MaxTranscodes: 1, RequestsAllowed: true}
	sm := playback.NewSessionManager(6, 2)
	sm.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		effective := access.ApplyGroupPolicy(user, group)
		return playback.SessionLimits{
			MaxStreams:    effective.MaxStreams,
			MaxTranscodes: effective.MaxTranscodes,
		}, nil
	})

	if _, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false); err != nil {
		t.Fatalf("StartSession first stream: %v", err)
	}
	if _, err := sm.StartSession(1, "profile-1", 101, playback.PlayDirect, false); err != playback.ErrTooManyStreams {
		t.Fatalf("StartSession second stream = %v, want ErrTooManyStreams", err)
	}
}

func TestSessionManager_UserLimitProviderAppliesTranscodeLimitOnlyToTranscodes(t *testing.T) {
	sm := playback.NewSessionManager(6, 2)
	sm.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{MaxStreams: 3, MaxTranscodes: 1}, nil
	})

	if _, err := sm.StartSession(1, "profile-1", 100, playback.PlayTranscode, false); err != nil {
		t.Fatalf("StartSession transcode: %v", err)
	}
	if _, err := sm.StartSession(1, "profile-1", 101, playback.PlayTranscode, false); err != playback.ErrTooManyTranscodes {
		t.Fatalf("StartSession over transcode limit = %v, want ErrTooManyTranscodes", err)
	}
	if _, err := sm.StartSession(1, "profile-1", 102, playback.PlayDirect, false); err != nil {
		t.Fatalf("StartSession direct while transcode limit hit: %v", err)
	}
}

func TestSessionManager_DisabledVideoTranscodingAllowsAudioByDefault(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)
	sm.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{TranscodingDisabled: true}, nil
	})

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
	sm := playback.NewSessionManager(0, 0)
	sm.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{
			TranscodingDisabled:      true,
			AudioTranscodingDisabled: true,
		}, nil
	})

	_, err := sm.StartSession(1, "profile-1", 100, playback.PlayRemux, true)
	if !errors.Is(err, playback.ErrAudioTranscodingDisabled) {
		t.Fatalf("StartSession() error = %v, want ErrAudioTranscodingDisabled", err)
	}
}

func TestSessionManager_CheckTranscodingAllowed(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)
	sm.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{
			TranscodingDisabled:      true,
			AudioTranscodingDisabled: true,
		}, nil
	})

	if err := sm.CheckTranscodingAllowed(context.Background(), 1, true); !errors.Is(err, playback.ErrTranscodingDisabled) {
		t.Fatalf("CheckTranscodingAllowed() error = %v, want ErrTranscodingDisabled", err)
	}
	if err := sm.CheckTranscodingAllowed(context.Background(), 1, false); !errors.Is(err, playback.ErrAudioTranscodingDisabled) {
		t.Fatalf("CheckTranscodingAllowed(audio) error = %v, want ErrAudioTranscodingDisabled", err)
	}
}

func TestSessionManager_PolicyAllowsAudioOnlyTranscodeWhenVideoTranscodingDisabled(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)
	sm.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{TranscodingDisabled: true}, nil
	})
	sm.SetAdmissionDecider(policy.NewPlaybackAdmissionDecider(newPlaybackPolicyPDP(t)))

	if _, err := sm.StartSession(1, "profile-1", 100, playback.PlayRemux, true); err != nil {
		t.Fatalf("StartSession(audio transcode) error = %v, want nil", err)
	}
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
							legacy := seededSessionManager(t, activeStreams, activeTranscodes)
							legacy.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
								return limits, nil
							})
							withPolicy := seededSessionManager(t, activeStreams, activeTranscodes)
							withPolicy.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
								return limits, nil
							})
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
		if _, err := sm.StartSession(1, "profile-1", 100+i, playback.PlayTranscode, false); err != nil {
			t.Fatalf("seed transcode %d: %v", i, err)
		}
	}
	for i := activeTranscodes; i < activeStreams; i++ {
		if _, err := sm.StartSession(1, "profile-1", 100+i, playback.PlayDirect, false); err != nil {
			t.Fatalf("seed direct %d: %v", i, err)
		}
	}
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
	_, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("User1 session 1: %v", err)
	}
	_, err = sm.StartSession(1, "profile-1", 101, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("User1 session 2: %v", err)
	}

	// User 1 should be blocked.
	_, err = sm.StartSession(1, "profile-1", 102, playback.PlayDirect, false)
	if err != playback.ErrTooManyStreams {
		t.Errorf("User1 session 3 = %v, want ErrTooManyStreams", err)
	}

	// User 2 should be unaffected.
	_, err = sm.StartSession(2, "profile-2", 200, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("User2 session 1: %v", err)
	}
	_, err = sm.StartSession(2, "profile-2", 201, playback.PlayTranscode, false)
	if err != nil {
		t.Fatalf("User2 session 2 (transcode): %v", err)
	}

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
	_, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	_, err = sm.StartSession(1, "profile-1", 101, playback.PlayTranscode, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	_, err = sm.StartSession(2, "profile-2", 200, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

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
	s1, _ := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	s2, _ := sm.StartSession(1, "profile-1", 101, playback.PlayRemux, false)
	s3, _ := sm.StartSession(2, "profile-2", 200, playback.PlayTranscode, false)

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

func TestSetTranscodeNodeURL(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	session, err := mgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}

	url := "http://transcode-1:8070"
	if err := mgr.SetTranscodeNodeURL(session.ID, url); err != nil {
		t.Fatalf("SetTranscodeNodeURL: %v", err)
	}

	got, err := mgr.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TranscodeNodeURL != url {
		t.Errorf("TranscodeNodeURL = %q, want %q", got.TranscodeNodeURL, url)
	}
}

func TestSetTranscodeNodeURL_NotFound(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	err := mgr.SetTranscodeNodeURL("nonexistent", "http://node:8070")
	if err != playback.ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSetEffectiveMediaFileID(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	session, err := mgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.SetEffectiveMediaFileID(session.ID, 200); err != nil {
		t.Fatalf("SetEffectiveMediaFileID: %v", err)
	}

	got, err := mgr.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MediaFileID != 200 {
		t.Errorf("MediaFileID = %d, want 200", got.MediaFileID)
	}
	if got.RequestedMediaFileID != 100 {
		t.Errorf("RequestedMediaFileID = %d, want 100", got.RequestedMediaFileID)
	}
}

func TestSessionReplacementAppliesAndRollsBackAtomically(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSessionWithFiles(7, "profile-1", 42, 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:           playback.PlayDirect,
		BasePlayMethod:       playback.PlayDirect,
		AudioTrackIndex:      0,
		TranscodeRouteSet:    true,
		SubtitleTrackIndex:   -1,
		StreamBitrateKbps:    8_000,
		TranscodeNodeURL:     "http://old-node",
		TranscodeTransportID: "old-transport",
	}); err != nil {
		t.Fatal(err)
	}
	position := 321.5
	rollback, err := manager.ApplyReplacement(session.ID, playback.SessionReplacement{
		EffectiveMediaFileID: 84,
		StreamState: playback.SessionStreamState{
			PlayMethod:           playback.PlayTranscode,
			BasePlayMethod:       playback.PlayTranscode,
			AudioTrackIndex:      2,
			TranscodeAudio:       true,
			TranscodeRouteSet:    true,
			SubtitleTrackIndex:   1,
			StreamBitrateKbps:    3_500,
			TranscodeNodeURL:     "http://new-node",
			TranscodeTransportID: "new-transport",
		},
		PositionSeconds: &position,
		IsPaused:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := manager.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.MediaFileID != 84 || replaced.PlayMethod != playback.PlayTranscode || replaced.AudioTrackIndex != 2 ||
		replaced.TranscodeNodeURL != "http://new-node" || replaced.Position != position || !replaced.IsPaused {
		t.Fatalf("replacement session = %#v", replaced)
	}
	if err := manager.RollbackReplacement(session.ID, rollback); err != nil {
		t.Fatal(err)
	}
	restored, err := manager.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.MediaFileID != 42 || restored.PlayMethod != playback.PlayDirect || restored.AudioTrackIndex != 0 ||
		restored.TranscodeNodeURL != "http://old-node" || restored.TranscodeTransportID != "old-transport" ||
		restored.Position != 0 || restored.IsPaused {
		t.Fatalf("restored session = %#v", restored)
	}
}

func TestSessionReplacementRollbackRejectsNewerMutation(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
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

func TestStartSessionWithFiles(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	session, err := mgr.StartSessionWithFiles(1, "profile-1", 200, 100, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSessionWithFiles: %v", err)
	}

	if session.MediaFileID != 200 {
		t.Errorf("MediaFileID = %d, want 200", session.MediaFileID)
	}
	if session.RequestedMediaFileID != 100 {
		t.Errorf("RequestedMediaFileID = %d, want 100", session.RequestedMediaFileID)
	}
	if session.BasePlayMethod != playback.PlayRemux {
		t.Errorf("BasePlayMethod = %q, want %q", session.BasePlayMethod, playback.PlayRemux)
	}
	if !session.TranscodeAudio {
		t.Error("TranscodeAudio = false, want true")
	}
}

func TestUpdateStreamState(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	session, err := mgr.StartSession(1, "profile-1", 100, playback.PlayRemux, false)
	if err != nil {
		t.Fatal(err)
	}

	err = mgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:           playback.PlayTranscode,
		BasePlayMethod:       playback.PlayRemux,
		AudioTrackIndex:      2,
		TranscodeAudio:       true,
		ClientIP:             "10.0.0.10",
		StreamBitrateKbps:    4200,
		TargetResolution:     "1080p",
		TargetVideoCodec:     "h264",
		TargetAudioCodec:     "aac",
		TargetBitrateKbps:    4000,
		TranscodeNodeURL:     "http://node-1",
		TranscodeTransportID: "transport-1",
		TranscodeRouteSet:    true,
		SubtitleTrackIndex:   3,
		SubtitleBurnIn:       true,
		SegmentDuration:      4,
	})
	if err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}

	got, err := mgr.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := mgr.UpdateStreamState(session.ID, playback.SessionStreamState{TranscodeRouteSet: true}); err != nil {
		t.Fatal(err)
	}
	got, err = mgr.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TranscodeNodeURL != "" || got.TranscodeTransportID != "" {
		t.Fatalf("cleared transcode route = %q/%q", got.TranscodeNodeURL, got.TranscodeTransportID)
	}
}

func TestUpdateAudioTrack_PreservesTranscodeTransportForRemuxBase(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	session, err := mgr.StartSession(1, "profile-1", 100, playback.PlayRemux, false)
	if err != nil {
		t.Fatal(err)
	}

	err = mgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:      playback.PlayTranscode,
		BasePlayMethod:  playback.PlayRemux,
		AudioTrackIndex: 0,
	})
	if err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}

	err = mgr.UpdateAudioTrack(session.ID, 1, playback.PlayRemux)
	if err != nil {
		t.Fatalf("UpdateAudioTrack: %v", err)
	}

	got, err := mgr.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
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

func TestSessionManager_WebSocketTracking(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// New sessions should not have WebSocket.
	got, _ := sm.GetSession(session.ID)
	if got.HasWebSocket {
		t.Error("new session should not have WebSocket")
	}

	// Set WebSocket connected.
	if err := sm.SetWebSocket(session.ID, true); err != nil {
		t.Fatalf("SetWebSocket(true): %v", err)
	}

	got, _ = sm.GetSession(session.ID)
	if !got.HasWebSocket {
		t.Error("HasWebSocket should be true after SetWebSocket(true)")
	}

	// Disconnect WebSocket.
	if err := sm.SetWebSocket(session.ID, false); err != nil {
		t.Fatalf("SetWebSocket(false): %v", err)
	}

	got, _ = sm.GetSession(session.ID)
	if got.HasWebSocket {
		t.Error("HasWebSocket should be false after SetWebSocket(false)")
	}
}

func TestSetRealtimeConnection(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

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

	if err := sm.SetRealtimeConnection(session.ID, false); err != nil {
		t.Fatalf("SetRealtimeConnection(false): %v", err)
	}

	got, _ = sm.GetSession(session.ID)
	if got.HasRealtimeConnection {
		t.Error("HasRealtimeConnection should be false after SetRealtime(false)")
	}
	if got.HasWebSocket {
		t.Error("HasWebSocket should clear when realtime connection closes")
	}
}

func TestSessionManager_LimitCountsIgnoreStaleSessions(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)
	sm.SetLivenessGracePeriods(20*time.Millisecond, 40*time.Millisecond)

	if _, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false); err != nil {
		t.Fatalf("StartSession direct: %v", err)
	}
	if _, err := sm.StartSession(1, "profile-1", 101, playback.PlayTranscode, false); err != nil {
		t.Fatalf("StartSession transcode: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	if got := sm.ActiveCount(1); got != 0 {
		t.Fatalf("ActiveCount after grace = %d, want 0", got)
	}
	if got := sm.TranscodeCount(1); got != 0 {
		t.Fatalf("TranscodeCount after grace = %d, want 0", got)
	}
}

func TestSessionManager_ActiveTransportKeepsSessionLive(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)
	sm.SetLivenessGracePeriods(20*time.Millisecond, 40*time.Millisecond)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sm.BeginTransport(session.ID); err != nil {
		t.Fatalf("BeginTransport: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	if got := sm.ActiveCount(1); got != 1 {
		t.Fatalf("ActiveCount with active transport = %d, want 1", got)
	}

	if err := sm.EndTransport(session.ID); err != nil {
		t.Fatalf("EndTransport: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	if got := sm.ActiveCount(1); got != 0 {
		t.Fatalf("ActiveCount after transport ends = %d, want 0", got)
	}
}

func TestRealtimeDisconnectDoesNotStopSession(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if err := sm.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection(true): %v", err)
	}
	if err := sm.SetRealtimeConnection(session.ID, false); err != nil {
		t.Fatalf("SetRealtimeConnection(false): %v", err)
	}

	got, err := sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession after realtime disconnect: %v", err)
	}
	if got.ID != session.ID {
		t.Fatalf("GetSession ID = %q, want %q", got.ID, session.ID)
	}
	if sm.ActiveCount(1) != 1 {
		t.Errorf("ActiveCount after realtime disconnect = %d, want 1", sm.ActiveCount(1))
	}
}

func TestGetSessionIncludesRealtimeState(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if err := sm.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection(true): %v", err)
	}

	got, err := sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !got.HasRealtimeConnection {
		t.Error("GetSession copy should include realtime connection state")
	}
	got.HasRealtimeConnection = false

	again, err := sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession after mutating copy: %v", err)
	}
	if !again.HasRealtimeConnection {
		t.Error("mutating GetSession copy should not change manager state")
	}
}

func TestSessionManager_SetWebSocket_NotFound(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	err := sm.SetWebSocket("nonexistent", true)
	if err != playback.ErrSessionNotFound {
		t.Errorf("SetWebSocket(nonexistent) = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionManager_SetRealtimeConnection_NotFound(t *testing.T) {
	sm := playback.NewSessionManager(5, 2)

	err := sm.SetRealtimeConnection("nonexistent", true)
	if err != playback.ErrSessionNotFound {
		t.Errorf("SetRealtimeConnection(nonexistent) = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionManager_ZeroLimits(t *testing.T) {
	// Zero limits means unlimited.
	sm := playback.NewSessionManager(0, 0)

	for i := range 10 {
		_, err := sm.StartSession(1, "profile-1", i, playback.PlayTranscode, false)
		if err != nil {
			t.Fatalf("StartSession %d with zero limits: %v", i, err)
		}
	}

	if sm.ActiveCount(1) != 10 {
		t.Errorf("ActiveCount = %d, want 10", sm.ActiveCount(1))
	}
}

func TestSessionManager_CleanExpired(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)

	// Create three sessions.
	idle, _ := sm.StartSession(1, "prof", 100, playback.PlayDirect, false)
	active, _ := sm.StartSession(1, "prof", 101, playback.PlayDirect, false)
	transportActive, _ := sm.StartSession(1, "prof", 102, playback.PlayDirect, false)

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
	if err != playback.ErrSessionNotFound {
		t.Errorf("idle session should be expired, got: %v", err)
	}
	_, err = sm.GetSession(active.ID)
	if err != playback.ErrSessionNotFound {
		t.Errorf("active session should be expired with maxIdle=0, got: %v", err)
	}

	// Only 1 session should remain (the transport-active one).
	if sm.ActiveCount(1) != 1 {
		t.Errorf("ActiveCount = %d, want 1", sm.ActiveCount(1))
	}
}

func TestSessionManager_CleanExpired_RespectsMaxIdle(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)

	// Create a session and immediately update it.
	s, _ := sm.StartSession(1, "prof", 100, playback.PlayDirect, false)
	_ = sm.UpdateProgress(s.ID, 10.0, false)

	// With a generous maxIdle, the session should survive.
	expired := sm.CleanExpired(time.Hour)
	if len(expired) != 0 {
		t.Errorf("CleanExpired(1h) removed %d sessions, want 0", len(expired))
	}

	if sm.ActiveCount(1) != 1 {
		t.Errorf("ActiveCount = %d, want 1", sm.ActiveCount(1))
	}
}

func TestSessionManager_CleanInactive_TriggersExpirationHook(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)

	session, err := sm.StartSession(1, "prof", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	expiredCh := make(chan string, 1)
	sm.SetExpirationHook(func(session *playback.Session) {
		expiredCh <- session.ID
	})

	expired := sm.CleanInactive(0, 0)
	if len(expired) != 1 {
		t.Fatalf("CleanInactive removed %d sessions, want 1", len(expired))
	}

	select {
	case got := <-expiredCh:
		if got != session.ID {
			t.Fatalf("expiration hook session = %q, want %q", got, session.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("expiration hook was not called")
	}
}

func TestSessionManager_CleanExpired_PausedGracePeriod(t *testing.T) {
	sm := playback.NewSessionManager(0, 0)

	// Create two sessions and mark one as paused.
	playing, _ := sm.StartSession(1, "prof", 100, playback.PlayDirect, false)
	paused, _ := sm.StartSession(1, "prof", 101, playback.PlayDirect, false)
	_ = sm.UpdateProgress(paused.ID, 50.0, true)

	// Let both sessions age past the base maxIdle but within the 3x
	// paused grace period. With 20ms maxIdle: cutoff = now-20ms,
	// pausedCutoff = now-60ms. After sleeping 50ms, sessions created
	// 50ms ago are older than cutoff (20ms) but newer than
	// pausedCutoff (60ms).
	time.Sleep(50 * time.Millisecond)

	expired := sm.CleanExpired(20 * time.Millisecond)

	// Only the playing session should be expired.
	if len(expired) != 1 {
		t.Fatalf("CleanExpired removed %d sessions, want 1", len(expired))
	}
	if expired[0].ID != playing.ID {
		t.Fatalf("expected playing session %s to be expired, got %s", playing.ID, expired[0].ID)
	}

	// Paused session should still exist.
	if _, err := sm.GetSession(paused.ID); err != nil {
		t.Errorf("paused session should survive with 3x grace period, got: %v", err)
	}
}

// A failed transcode session idle past the active grace is already absent from
// the live counts; replacement admission must exclude that session explicitly
// rather than decrement the totals, or the decrement frees a slot that belongs
// to another live transcode.
func TestCheckReplacementAllowedExcludesOnlyTheReplacedSession(t *testing.T) {
	sm := playback.NewSessionManager(10, 2)
	sm.SetLivenessGracePeriods(25*time.Millisecond, time.Hour)

	failed, err := sm.StartSession(1, "profile-1", 100, playback.PlayTranscode, false)
	if err != nil {
		t.Fatalf("StartSession(failed): %v", err)
	}
	// Age the failed session past the active grace so it no longer counts.
	time.Sleep(60 * time.Millisecond)

	for i := 0; i < 2; i++ {
		if _, err := sm.StartSession(1, "profile-1", 200+i, playback.PlayTranscode, false); err != nil {
			t.Fatalf("StartSession(live %d): %v", i, err)
		}
	}

	err = sm.CheckReplacementAllowed(context.Background(), failed.ID, playback.PlayTranscode, false)
	if !errors.Is(err, playback.ErrTooManyTranscodes) {
		t.Fatalf("CheckReplacementAllowed = %v, want ErrTooManyTranscodes (both slots are held by live sessions)", err)
	}
}

// Legacy stream updates (audio switches, progress-driven state) arriving while
// a v3 replacement is in flight must not release the capacity reservation;
// only the replacement's own route-set commit consumes it.
func TestLegacyStreamUpdateKeepsReplacementReservation(t *testing.T) {
	sm := playback.NewSessionManager(10, 1)

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
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

	session, err := sm.StartSession(1, "profile-1", 100, playback.PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{RemuxDVMode: playback.RemuxDVStripToHDR10V3, TranscodeRouteSet: true}); err != nil {
		t.Fatalf("UpdateStreamState(set): %v", err)
	}

	// Legacy partial update: mode survives.
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{AudioTrackIndex: 1}); err != nil {
		t.Fatalf("UpdateStreamState(legacy): %v", err)
	}
	got, err := sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.RemuxDVMode != playback.RemuxDVStripToHDR10V3 {
		t.Fatalf("RemuxDVMode after legacy update = %q, want strip_to_hdr10", got.RemuxDVMode)
	}

	// v3 route-set update for a non-DV source: mode clears.
	if err := sm.UpdateStreamState(session.ID, playback.SessionStreamState{TranscodeRouteSet: true}); err != nil {
		t.Fatalf("UpdateStreamState(clear): %v", err)
	}
	got, err = sm.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.RemuxDVMode != "" {
		t.Fatalf("RemuxDVMode after route-set update = %q, want cleared", got.RemuxDVMode)
	}
}
