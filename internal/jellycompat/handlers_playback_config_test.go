package jellycompat

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPlaybackHandlerReadsLiveSegmentRetention(t *testing.T) {
	cfg := &config.Config{Playback: config.PlaybackConfig{SegmentRetentionSeconds: 300}}
	handler := NewPlaybackHandler(cfg, nil, nil, nil, nil, nil, nil, nil)

	if got := handler.tm.Config().SegmentRetentionSeconds; got != 300 {
		t.Fatalf("configured segment retention = %d, want 300", got)
	}
	liveRetention := 300
	handler.SegmentRetentionSeconds = func() int { return liveRetention }
	liveRetention = 120
	if got := handler.tm.Config().SegmentRetentionSeconds; got != 120 {
		t.Fatalf("reloaded segment retention = %d, want 120", got)
	}
}

func TestNewPlaybackHandler_ExpiresCompatTranscode(t *testing.T) {
	sessions := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(nil, nil, nil, nil, nil, sessions, nil, nil)

	session, err := sessions.StartSession(1, "profile", 100, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler.tm.RegisterTranscodeSession(session.ID, &playback.TranscodeSession{})

	sessions.CleanInactive(0, 0)
	deadline := time.After(time.Second)
	for handler.tm.GetTranscodeSession(session.ID) != nil {
		select {
		case <-deadline:
			t.Fatal("expired Jellyfin-compatible session left its transcode registered")
		default:
			runtime.Gosched()
		}
	}
}

func TestNewPlaybackHandler_UsesConfiguredTranscodeThrottle(t *testing.T) {
	handler := NewPlaybackHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	if handler.tm.StartThrottler == nil {
		t.Fatal("Jellyfin-compatible transcode manager has no throttle callback")
	}

	// The callback must tolerate a disabled/unconfigured settings repository.
	handler.tm.StartThrottler(context.Background(), &playback.TranscodeSession{})
}

func TestEnsureTranscodeSession_StartsThrottleForFreshCompatRemux(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	modifiedAt := info.ModTime()
	probeUpdatedAt := time.Now().UTC()
	file := &models.MediaFile{
		ID: 42, FilePath: mediaPath, FileSize: info.Size(), FileModifiedAt: &modifiedAt,
		FileHash: "hash", ProbeUpdatedAt: &probeUpdatedAt,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
	}

	ffmpegPath := filepath.Join(dir, "ffmpeg")
	ffmpegScript := "#!/bin/sh\n" +
		"out=''\n" +
		"for arg in \"$@\"; do case \"$arg\" in *.m3u8) out=\"$(dirname \"$arg\")\";; esac; done\n" +
		"mkdir -p \"$out\"\n" +
		"printf init > \"$out/init.mp4\"\n" +
		"for name in seg_00000.m4s seg_00001.m4s seg_00002.m4s; do printf segment > \"$out/$name\"; done\n" +
		"printf '#EXTM3U\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n#EXTINF:2,\\nseg_00000.m4s\\n#EXTINF:2,\\nseg_00001.m4s\\n#EXTINF:2,\\nseg_00002.m4s\\n' > \"$out/stream.m3u8\"\n" +
		"sleep 30\n"
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}

	version := testCompatVersion()
	source := testCompatSource(NewResourceIDCodec(), version)
	source.HLSRemux = true
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
		MediaSources: []PlaybackMediaSource{source},
	})
	sessions := playback.NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&playback.Session{
		ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode,
	})
	handler := NewPlaybackHandler(nil, nil, nil, nil, store, sessions, testCompatFileResolver{file: file}, nil)
	handler.TranscodeDir = filepath.Join(dir, "transcode")
	handler.FFmpegPath = ffmpegPath
	started := make(chan *playback.TranscodeSession, 1)
	handler.tm.StartThrottler = func(_ context.Context, session *playback.TranscodeSession) {
		started <- session
	}

	transcode, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatalf("ensureTranscodeSession: %v", err)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession("upstream-1", "") })
	select {
	case got := <-started:
		if got != transcode {
			t.Fatal("throttle started for a different transcode session")
		}
	case <-time.After(time.Second):
		t.Fatal("fresh Jellyfin-compatible remux did not start the transcode throttle")
	}
}
