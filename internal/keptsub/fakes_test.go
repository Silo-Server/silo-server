package keptsub

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// memRepo is Repo in memory.
type memRepo struct {
	mu     sync.Mutex
	tracks map[trackKey]Track
	claims map[trackKey]memClaim
	// counts
	puts, claimCalls, fileTrackCalls int
	// queued lists the keys queued for collection; putErr fails PutTrack.
	queued []string
	putErr error
	// trackHook, when set, runs at the start of every Track call.
	trackHook func(ctx context.Context) error
}

type memClaim struct {
	token   string
	expires time.Time
}

func newMemRepo() *memRepo {
	return &memRepo{tracks: map[trackKey]Track{}, claims: map[trackKey]memClaim{}}
}

func (r *memRepo) Track(ctx context.Context, fileID, stream int) (*Track, error) {
	if r.trackHook != nil {
		if err := r.trackHook(ctx); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tracks[trackKey{fileID, stream}]
	if !ok {
		return nil, nil
	}
	return &t, nil
}

func (r *memRepo) FileTracks(_ context.Context, fileID int) ([]Track, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fileTrackCalls++
	var out []Track
	for k, t := range r.tracks {
		if k.fileID == fileID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *memRepo) PutTrack(_ context.Context, t Track) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.putErr != nil {
		return r.putErr
	}
	r.puts++
	r.tracks[trackKey{t.MediaFileID, t.StreamIndex}] = t
	return nil
}

func (r *memRepo) DeleteTrack(_ context.Context, fileID, stream int, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tracks[trackKey{fileID, stream}]; ok && t.BlobKey == key {
		delete(r.tracks, trackKey{fileID, stream})
	}
	return nil
}

func (r *memRepo) Claim(_ context.Context, fileID, stream int, token string, lease time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimCalls++
	k := trackKey{fileID, stream}
	if c, ok := r.claims[k]; ok && time.Now().Before(c.expires) {
		return false, nil
	}
	r.claims[k] = memClaim{token: token, expires: time.Now().Add(lease)}
	return true, nil
}

func (r *memRepo) Release(_ context.Context, fileID, stream int, token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := trackKey{fileID, stream}
	if r.claims[k].token == token {
		delete(r.claims, k)
	}
	return nil
}

func (r *memRepo) QueueCollection(_ context.Context, key string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queued = append(r.queued, key)
	return nil
}

func (r *memRepo) FileKeptBytes(_ context.Context, fileID, except int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for k, t := range r.tracks {
		if k.fileID == fileID && k.stream != except {
			total += t.Bytes
		}
	}
	return total, nil
}

func (r *memRepo) KeptBytes(context.Context) (map[string]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byCodec := map[string]int64{}
	for _, t := range r.tracks {
		byCodec[t.Codec] += t.Bytes
	}
	return byCodec, nil
}

func (r *memRepo) counts() (puts, claims int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.puts, r.claimCalls
}

// memBlobs is Blobs in memory.
type memBlobs struct {
	mu      sync.Mutex
	objects map[string][]byte
	gets    int
	puts    int
	// getHook, when set, runs at the start of every Get.
	getHook func()
}

func (b *memBlobs) PutStream(_ context.Context, key string, r io.Reader, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.objects == nil {
		b.objects = map[string][]byte{}
	}
	b.puts++
	b.objects[key] = data
	return nil
}

func (b *memBlobs) Get(_ context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	if b.getHook != nil {
		b.getHook()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets++
	data, ok := b.objects[key]
	if !ok {
		return nil, blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), blobstore.ObjectInfo{Key: key, Size: int64(len(data))}, nil
}

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	return ffmpeg
}

const (
	srtTagged = "1\n00:00:00,500 --> 00:00:02,000\n{\\an8}At the top\n\n2\n00:00:03,000 --> 00:00:04,250\nSecond\n\n3\n00:00:10,100 --> 00:00:11,000\nLast\n"
	srtPlain  = "1\n00:00:01,000 --> 00:00:02,500\nErzwungen\n\n2\n00:00:06,000 --> 00:00:07,000\nNoch einer\n"
)

// fixtureFile writes a Matroska file with video, a tagged English SubRip
// track and a forced German one: tracks 2 and 3, streams 1 and 2. Without
// cues, FFmpeg writes it to a pipe, which leaves the Cues out.
func fixtureFile(t *testing.T, ffmpeg string, cues bool) (string, *models.MediaFile) {
	t.Helper()
	dir := t.TempDir()
	en, de := filepath.Join(dir, "en.srt"), filepath.Join(dir, "de.srt")
	for p, data := range map[string]string{en: srtTagged, de: srtPlain} {
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "in.mkv")
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x36:rate=10", "-i", en, "-i", de, "-t", "12",
		"-map", "0:v", "-map", "1", "-map", "2", "-c:v", "mpeg4", "-c:s", "srt",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=ger", "-disposition:s:1", "forced",
		"-f", "matroska"}
	cmd := exec.Command(ffmpeg, append(args, out)...)
	if !cues {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		cmd = exec.Command(ffmpeg, append(args, "pipe:1")...)
		cmd.Stdout = f
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("ffmpeg can't make the fixture (%v): %s", err, stderr.Bytes())
	}
	file := &models.MediaFile{ID: 41, FilePath: out, Container: "mkv", SubtitleTracks: []models.SubtitleTrack{
		{Index: 1, ContainerTrackID: "2", Codec: "subrip", Language: "eng"},
		{Index: 2, ContainerTrackID: "3", Codec: "subrip", Language: "ger", Forced: true},
	}}
	if info, err := os.Stat(out); err == nil {
		file.FileSize = info.Size()
		file.FileModifiedAt = new(info.ModTime())
	}
	return out, file
}

func newTestStore(t *testing.T, ffmpeg string) (*Store, *memRepo, *memBlobs) {
	t.Helper()
	repo, blobs := newMemRepo(), &memBlobs{}
	mirror := filepath.Join(t.TempDir(), "kept")
	s := NewStore(repo, blobs, Options{FFmpegPath: func() string { return ffmpeg }, MirrorDir: func() string { return mirror }})
	s.pollInterval = 10 * time.Millisecond
	return s, repo, blobs
}

// convert runs today's WebVTT conversion on in.
func convert(t *testing.T, ffmpeg string, in *playback.SubtitlePacketInput) string {
	t.Helper()
	var out bytes.Buffer
	if err := playback.StreamExtractSubtitle(t.Context(), playback.StreamExtractOpts{Packets: in, SourceCodec: "subrip", FFmpegPath: ffmpeg, Writer: &out}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// demux runs today's WebVTT extraction from the source, optionally a window.
func demux(t *testing.T, ffmpeg, path string, ordinal int, seek, duration float64) string {
	t.Helper()
	var out bytes.Buffer
	if err := playback.StreamExtractSubtitle(t.Context(), playback.StreamExtractOpts{
		InputPath: path, TrackIndex: ordinal, SourceCodec: "subrip", SeekSeconds: seek, DurationSeconds: duration, FFmpegPath: ffmpeg, Writer: &out,
	}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
