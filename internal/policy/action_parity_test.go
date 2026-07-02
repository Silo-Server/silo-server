package policy_test

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/policy"
)

func TestActionParityDownloads(t *testing.T) {
	pdp := newActionParityPDP(t)
	ctx := context.Background()
	resolver := downloads.DownloadQualityResolver{}

	for _, downloadsEnabled := range []bool{false, true} {
		for _, transcodeEnabled := range []bool{false, true} {
			for _, downloadAllowed := range []bool{false, true} {
				for _, downloadTranscodeAllowed := range []bool{false, true} {
					for _, artifactsAvailable := range []bool{false, true} {
						cfg := config.DownloadConfig{
							Enabled:          downloadsEnabled,
							TranscodeEnabled: transcodeEnabled,
						}
						user := &models.User{
							ID:                       42,
							DownloadAllowed:          downloadAllowed,
							DownloadTranscodeAllowed: downloadTranscodeAllowed,
						}
						presets := resolver.PresetsFor(user, cfg, artifactsAvailable)
						input := policy.ActionInput{
							SchemaVersion:            1,
							UserID:                   user.ID,
							DownloadAllowed:          user.DownloadAllowed,
							DownloadTranscodeAllowed: user.DownloadTranscodeAllowed,
							DownloadsEnabled:         cfg.Enabled,
							TranscodeEnabled:         cfg.TranscodeEnabled,
							ArtifactsAvailable:       artifactsAvailable,
						}

						downloadInput := input
						downloadInput.Action = policy.ActionDownload
						downloadDecision, _, err := pdp.CheckAction(ctx, downloadInput)
						if err != nil {
							t.Fatalf("CheckAction(download) error: %v", err)
						}
						if got, want := downloadDecision.Allowed, len(presets) > 0; got != want {
							t.Fatalf("download allowed = %v, want %v for cfg=%+v user=%+v artifacts=%v presets=%v",
								got, want, cfg, user, artifactsAvailable, presets)
						}

						transcodeInput := input
						transcodeInput.Action = policy.ActionDownloadTranscode
						transcodeDecision, _, err := pdp.CheckAction(ctx, transcodeInput)
						if err != nil {
							t.Fatalf("CheckAction(download_transcode) error: %v", err)
						}
						if got, want := transcodeDecision.Allowed, len(presets) > 1; got != want {
							t.Fatalf("download_transcode allowed = %v, want %v for cfg=%+v user=%+v artifacts=%v presets=%v",
								got, want, cfg, user, artifactsAvailable, presets)
						}
					}
				}
			}
		}
	}
}

func TestActionParityPlaybackAdmission(t *testing.T) {
	pdp := newActionParityPDP(t)
	ctx := context.Background()

	for _, maxStreams := range []int{0, 1, 2} {
		for _, maxTranscodes := range []int{0, 1, 2} {
			for _, activeStreams := range []int{0, 1, 2} {
				for _, activeTranscodes := range []int{0, 1, 2} {
					for _, requested := range []string{policy.RequestedActionDirectPlay, policy.RequestedActionTranscode} {
						limits := playback.SessionLimits{MaxStreams: maxStreams, MaxTranscodes: maxTranscodes}
						input := policy.ActionInput{
							SchemaVersion:           1,
							Action:                  policy.ActionPlaybackAdmission,
							UserID:                  42,
							MaxStreams:              limits.MaxStreams,
							MaxTranscodes:           limits.MaxTranscodes,
							CurrentActiveStreams:    activeStreams,
							CurrentActiveTranscodes: activeTranscodes,
							RequestedAction:         requested,
						}
						decision, _, err := pdp.CheckAction(ctx, input)
						if err != nil {
							t.Fatalf("CheckAction(playback_admission) error: %v", err)
						}
						want := legacyAdmissionAllowed(limits, activeStreams, activeTranscodes, requested)
						if decision.Allowed != want {
							t.Fatalf("playback allowed = %v, want %v for limits=%+v streams=%d transcodes=%d requested=%s reason=%q",
								decision.Allowed, want, limits, activeStreams, activeTranscodes, requested, decision.Reason)
						}
					}
				}
			}
		}
	}
}

func legacyAdmissionAllowed(limits playback.SessionLimits, activeStreams, activeTranscodes int, requested string) bool {
	if limits.MaxStreams > 0 && activeStreams >= limits.MaxStreams {
		return false
	}
	if requested == policy.RequestedActionTranscode && limits.MaxTranscodes > 0 && activeTranscodes >= limits.MaxTranscodes {
		return false
	}
	return true
}

func newActionParityPDP(t *testing.T) *policy.PDP {
	t.Helper()
	engine, err := policy.NewEngine(context.Background())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	return policy.NewPDP(engine)
}
