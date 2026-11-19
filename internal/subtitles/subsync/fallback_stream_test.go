package subsync

import (
	"context"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// dubbedFixture is the file of #4385: two audio tracks without language
// tags, the default a dub whose speech does not follow the subtitle, the
// second the original the subtitle was translated from.
func dubbedFixture(t *testing.T, truth subtitles.Timing) (*fixture, *[]int) {
	t.Helper()
	f := newFixture(t, truth, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	f.svc.files.(fakeFiles).file.AudioTracks = []models.AudioTrack{
		{Codec: "ac3", Layout: "stereo", Default: true},
		{Codec: "ac3", Layout: "stereo"},
	}
	dub := dialogCues(77, 7200)
	original := f.svc.sampler.local
	var streams []int
	f.svc.sampler.local = func(ctx context.Context, req mediasample.Request) (mediasample.Result, error) {
		stream := req.Audio.Speech.AudioStream
		if !slices.Contains(streams, stream) {
			streams = append(streams, stream)
		}
		if stream == 0 {
			w := speechFor(dub, subtitles.Timing{Scale: 1}, []float64{req.Window.StartSeconds}, req.Window.DurationSeconds, uint64(req.Window.StartSeconds)+1)[0]
			return mediasample.Result{Speech: &w}, nil
		}
		return original(ctx, req)
	}
	return f, &streams
}

func TestExecuteListensToTheOtherStreamWhenTheDefaultIsADub(t *testing.T) {
	truth := subtitles.Timing{Scale: 1, OffsetMS: 2400}
	f, streams := dubbedFixture(t, truth)
	job := f.run(t, TriggerManual)
	if job.Status != string(StatusSynced) {
		t.Fatalf("status %s: %+v", job.Status, f.jobs.finished[job.ID])
	}
	if !slices.Equal(*streams, []int{0, 1}) {
		t.Fatalf("decoded streams %v, want the default then the original", *streams)
	}
	assertTiming(t, f.jobs.applied[job.ID], truth, 7200)
}

func TestExecuteStaysOnThePlannedStreamWhenItMatches(t *testing.T) {
	truth := subtitles.Timing{Scale: 1, OffsetMS: 2400}
	f, streams := dubbedFixture(t, truth)
	// Swap the tracks: the default is now the original.
	file := f.svc.files.(fakeFiles).file
	file.AudioTracks[0].Default, file.AudioTracks[1].Default = false, true
	job := f.run(t, TriggerManual)
	if job.Status != string(StatusSynced) || !slices.Equal(*streams, []int{1}) {
		t.Fatalf("status %s, decoded streams %v; want synced from stream 1 alone", job.Status, *streams)
	}
}

func TestExecuteStillReportsNoMatchWhenNoStreamMatches(t *testing.T) {
	f, streams := dubbedFixture(t, subtitles.Timing{Scale: 1})
	// The subtitle belongs to another title: neither stream follows it.
	f.svc.content.(*fakeSubtitles).data = subtitles.SerializeSRT(dialogCues(5, 7200))
	job := f.run(t, TriggerManual)
	if job.Status != string(StatusNoMatch) || len(f.jobs.applied) != 0 {
		t.Fatalf("status %s applied %v", job.Status, f.jobs.applied)
	}
	if !slices.Equal(*streams, []int{0, 1}) {
		t.Fatalf("decoded streams %v", *streams)
	}
}

func TestFallbackAudioStreamsSkipsThePlannedOneAndStopsAtTheBound(t *testing.T) {
	tracks := make([]models.AudioTrack, 6)
	for i := range tracks {
		tracks[i] = models.AudioTrack{Codec: "aac"}
	}
	tracks[2].Codec = "unknown" // ffprobe has no codec for it
	got := fallbackAudioStreams(tracks, 1)
	if want := []int{0, 3, 4}; !slices.Equal(got, want) {
		t.Fatalf("fallback streams %v, want %v", got, want)
	}
}
