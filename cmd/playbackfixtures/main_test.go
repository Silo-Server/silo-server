package main

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPlannerFixturesKeepRequestedAudioIdentity(t *testing.T) {
	for _, scenario := range goldenConformanceMatrix().Planner {
		t.Run(scenario.Name, func(t *testing.T) {
			request := scenario.Request
			if request.FileID != scenario.Source.MediaFileID {
				t.Fatalf("request file %d differs from source %d", request.FileID, scenario.Source.MediaFileID)
			}
			if request.AudioTrackIndex == nil {
				t.Fatal("fixture must declare its requested audio index")
			}
			want := playback.TrackIDV3(request.FileID, "audio", *request.AudioTrackIndex)
			if request.AudioTrackID != want {
				t.Errorf("requested audio ID = %q, want %q", request.AudioTrackID, want)
			}
			if selected := scenario.Expected.SelectedTracks; selected != nil && selected.Audio != nil {
				if selected.Audio.ID != request.AudioTrackID || selected.Audio.Index == nil || *selected.Audio.Index != *request.AudioTrackIndex {
					t.Errorf("selected audio %+v does not preserve requested ID %q/index %d", selected.Audio, request.AudioTrackID, *request.AudioTrackIndex)
				}
			}
		})
	}
}
