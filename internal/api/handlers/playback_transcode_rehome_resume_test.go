package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A resume moved to another node makes the keyframe plan decision its first
// start made: one that planned asks the new node to plan it again, and one
// that kept FFmpeg's playlist doesn't ask for a plan at all. A stream from the
// start keeps asking as before.
func TestRehomeStartRequestKeepsTheResumePlanDecision(t *testing.T) {
	for name, tc := range map[string]struct {
		origin               float64
		planned              bool
		wantKeyframePlaylist bool
	}{
		"resume that planned":       {origin: 13, planned: true, wantKeyframePlaylist: true},
		"resume that kept FFmpeg's": {origin: 13, wantKeyframePlaylist: false},
		"stream from the start":     {wantKeyframePlaylist: true},
	} {
		t.Run(name, func(t *testing.T) {
			card := playback.RecipeCard{
				TargetCodecVideo:       "copy",
				KeyframePlaylist:       true,
				StreamOriginSeconds:    tc.origin,
				CopySeekAnchorResolved: true,
				KeyframePlanResume:     tc.planned,
			}
			req := rehomeStartRequestV3(card, "transport", "", -1)
			if req.KeyframePlaylist != tc.wantKeyframePlaylist || req.KeyframePlanResume != tc.planned {
				t.Fatalf("request asks keyframe_playlist=%v keyframe_plan_resume=%v, want %v and %v",
					req.KeyframePlaylist, req.KeyframePlanResume, tc.wantKeyframePlaylist, tc.planned)
			}
		})
	}
}
