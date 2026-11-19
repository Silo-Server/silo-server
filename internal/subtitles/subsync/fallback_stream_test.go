package subsync

import (
	"context"
	"errors"
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

// A fallback stream that can't be read right now (a busy host, no node, a
// canceled run, a storage error) leaves the job unanswered: it fails with
// that error, retryable where it is, instead of ending no_match on a stream
// nobody heard. A stream the file itself can't decode is skipped, and the
// planned stream's no_match stands.
func TestExecuteKeepsAnUnreadFallbackStreamRetryable(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		wantStatus string
		wantFail   string
	}{
		"busy host": {err: &mediasample.Error{Reason: mediasample.ReasonTimeout,
			Attempts: []mediasample.AttemptError{{Reason: mediasample.ReasonTimeout, Err: errors.New("timed out")}}},
			wantStatus: JobFailed, wantFail: FailureUnavailable},
		"canceled":         {err: context.Canceled, wantStatus: JobFailed, wantFail: FailureUnavailable},
		"storage error":    {err: errors.New("load speech artifact: connection refused"), wantStatus: JobFailed, wantFail: FailureError},
		"unreadable track": {err: &mediasample.Error{Reason: mediasample.ReasonExit, Attempts: []mediasample.AttemptError{{Reason: mediasample.ReasonExit, Err: errors.New("exit status 1"), StderrTail: "Stream map '0:a:1' matches no streams."}}}, wantStatus: string(StatusNoMatch)},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := dubbedFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 2400})
			dub := f.svc.sampler.local
			f.svc.sampler.local = func(ctx context.Context, req mediasample.Request) (mediasample.Result, error) {
				if req.Audio.Speech.AudioStream == 1 {
					return mediasample.Result{}, tc.err
				}
				return dub(ctx, req)
			}
			job := f.run(t, TriggerManual)
			outcome := f.jobs.finished[job.ID]
			if outcome.Status != tc.wantStatus || outcome.Failure != tc.wantFail || len(f.jobs.applied) != 0 {
				t.Fatalf("outcome %+v applied %v, want status %s failure %q", outcome, f.jobs.applied, tc.wantStatus, tc.wantFail)
			}
		})
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
