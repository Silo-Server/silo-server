package heroclips

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// BlobWriter is the storage surface a clip is uploaded through. Satisfied by the
// assets store.
type BlobWriter interface {
	PutStream(ctx context.Context, key string, r io.Reader, contentType string) error
}

// ClipPublisher records what a generation produced. Satisfied by *Repository.
type ClipPublisher interface {
	PublishReady(ctx context.Context, clip Clip) error
	RecordFailure(ctx context.Context, contentID, recipe, source, sourceKey, message string) error
}

// Generator makes one title's hero clip: the trailer is downloaded, cut to the
// recipe, uploaded, and only then published.
//
// The order matters. The row is written last, so a client is never handed a URL
// for an object that is not there yet, and a run that dies mid-flight leaves an
// unreferenced object for the orphan sweep rather than a broken row.
type Generator struct {
	Publisher ClipPublisher
	Clips     BlobWriter
	// FFmpegPath locates the encoder. Empty means "ffmpeg" on the path.
	FFmpegPath func() string
	// YtDlpPath locates the downloader. Empty means SILO_YTDLP_PATH, then
	// "yt-dlp" on the path.
	YtDlpPath string
	// TempRoot stages downloads and encodes. Empty uses the system temporary
	// directory.
	TempRoot string
	Recipe   Recipe
}

// Create builds the clip for one candidate. A failure is recorded on the title
// and reported as nil: one unreachable trailer must not stop the run, and the
// row is what makes the generator back off instead of retrying every pass.
func (g *Generator) Create(ctx context.Context, c Candidate) error {
	if g.Publisher == nil || g.Clips == nil {
		return fmt.Errorf("hero clips: generator is not configured")
	}

	recipe := g.recipe()
	work, err := os.MkdirTemp(g.tempRoot(), "hero-clip-")
	if err != nil {
		return fmt.Errorf("stage hero clip: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	inputs, err := g.download(ctx, c, work)
	if err != nil {
		return g.fail(ctx, c, recipe, err)
	}

	clipPath := filepath.Join(work, "clip.mp4")
	if err := g.encode(ctx, inputs, clipPath); err != nil {
		return g.fail(ctx, c, recipe, err)
	}

	clip, err := g.inspect(ctx, clipPath)
	if err != nil {
		return g.fail(ctx, c, recipe, err)
	}
	if clip.DurationMS > MaxDurationSeconds*1000 {
		return g.fail(ctx, c, recipe, fmt.Errorf("trailer runs %ds, longer than a hero should hold",
			clip.DurationMS/1000))
	}
	if clip.Bytes > MaxBytes {
		return g.fail(ctx, c, recipe,
			fmt.Errorf("clip is %d bytes, larger than a hero should download", clip.Bytes))
	}

	file, err := os.Open(clipPath)
	if err != nil {
		return g.fail(ctx, c, recipe, err)
	}
	defer func() { _ = file.Close() }()

	clip.ContentID, clip.Path = c.ContentID, ClipKey(c.ContentID, recipe)
	clip.Recipe, clip.Source, clip.SourceKey = recipe.String(), c.Source, c.SiteKey

	if err := g.Clips.PutStream(ctx, clip.Path, file, "video/mp4"); err != nil {
		return g.fail(ctx, c, recipe, fmt.Errorf("upload %s: %w", clip.Path, err))
	}

	if err := g.Publisher.PublishReady(ctx, clip); err != nil {
		return g.fail(ctx, c, recipe, err)
	}

	slog.Log(ctx, slog.LevelInfo, "hero clip generated", "component", "heroclips",
		"content_id", c.ContentID, "path", clip.Path, "duration_ms", clip.DurationMS, "bytes", clip.Bytes)

	return nil
}

// download fetches the trailer and returns the files ffmpeg should read.
//
// The downloader muxes the video and audio parts it picks into the named file,
// but only when it can find an encoder itself, and the image keeps
// jellyfin-ffmpeg outside the search path it uses for that. So the encoder is
// named for it: without that the parts are never merged, every trailer costs a
// second pass through the encoder here, and a clip is lost outright wherever
// the downloader cannot find one at all.
func (g *Generator) download(ctx context.Context, c Candidate, work string) ([]string, error) {
	merged := filepath.Join(work, "source.mp4")
	url := youtubeWatchURL + c.SiteKey

	args := []string{"--no-playlist", "--no-warnings",
		"-S", fmt.Sprintf("res:%d", g.sourceHeight()),
		"--merge-output-format", "mp4",
		"-o", merged}
	// An unset path is left alone rather than guessed at: with no location given
	// the downloader searches its own path, which is where a deployment that
	// installs its own tools keeps them.
	if path := g.configuredFFmpegPath(); path != "" {
		args = append(args, "--ffmpeg-location", path)
	}
	args = append(args, url)

	if err := runCommand(ctx, g.ytdlpPath(), args...); err != nil {
		return nil, fmt.Errorf("download trailer: %w", err)
	}

	if _, err := os.Stat(merged); err == nil {
		return []string{merged}, nil
	}

	parts, err := downloadParts(work)
	if err != nil {
		return nil, fmt.Errorf("download trailer: %w", err)
	}

	return parts, nil
}

// encode cuts the downloaded trailer to the recipe. The whole trailer is kept:
// its length is the hero's length, so nobody is moved on mid-trailer.
func (g *Generator) encode(ctx context.Context, inputs []string, out string) error {
	args := []string{"-y", "-loglevel", "error"}
	for _, input := range inputs {
		args = append(args, "-i", input)
	}
	args = append(args,
		"-vf", fmt.Sprintf("scale=%d:-2", g.recipe().Width),
		"-c:v", "libx264",
		"-crf", strconv.Itoa(CRF),
		"-preset", "veryfast",
		"-pix_fmt", "yuv420p",
		// The element plays muted until a viewer asks for sound; the file keeps
		// its audio so that choice is possible at all.
		"-c:a", "aac",
		"-b:a", AudioBitrate,
		"-ac", "2",
		// Faststart moves the index to the front, so the hero can start playing
		// before the whole clip has arrived.
		"-movflags", "+faststart",
		out)

	if err := runCommand(ctx, g.ffmpegPath(), args...); err != nil {
		return fmt.Errorf("encode clip: %w", err)
	}

	return nil
}

// inspect measures the clip that was made: what a client would download, and
// what the row records about it.
func (g *Generator) inspect(ctx context.Context, path string) (Clip, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Clip{}, fmt.Errorf("stat clip: %w", err)
	}

	output, err := commandOutput(ctx, ffprobeOf(g.ffmpegPath()), "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-show_entries", "format=duration",
		"-of", "json", path)
	if err != nil {
		return Clip{}, fmt.Errorf("probe clip: %w", err)
	}

	var probed struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &probed); err != nil {
		return Clip{}, fmt.Errorf("read clip probe: %w", err)
	}

	clip := Clip{Bytes: info.Size()}
	if len(probed.Streams) > 0 {
		clip.Width, clip.Height = probed.Streams[0].Width, probed.Streams[0].Height
	}
	if seconds, err := strconv.ParseFloat(probed.Format.Duration, 64); err == nil && seconds > 0 {
		clip.DurationMS = int(seconds * 1000)
	}
	if clip.DurationMS <= 0 {
		return Clip{}, fmt.Errorf("clip reports no duration")
	}

	return clip, nil
}

// fail records why an item has no clip and reports the run as fine: the row is
// the backoff, and every other title in the run still gets its turn.
func (g *Generator) fail(ctx context.Context, c Candidate, recipe Recipe, err error) error {
	slog.Log(ctx, slog.LevelWarn, "hero clip failed", "component", "heroclips",
		"content_id", c.ContentID, "error", err)

	recordErr := g.Publisher.RecordFailure(ctx, c.ContentID, recipe.String(), c.Source, c.SiteKey, err.Error())
	if recordErr != nil {
		slog.Log(ctx, slog.LevelError, "recording hero clip failure", "component", "heroclips",
			"content_id", c.ContentID, "error", recordErr)
	}

	return nil
}

func (g *Generator) recipe() Recipe {
	if g.Recipe.Width == 0 {
		return NewRecipe(0)
	}
	return g.Recipe
}

// sourceHeight asks the downloader for no more than the clip can use: a hero
// clip is 1280 wide, so a 4K source would only cost time and bandwidth.
func (g *Generator) sourceHeight() int {
	return min(1080, max(g.recipe().Width, MinWidth))
}

func (g *Generator) tempRoot() string {
	if g.TempRoot == "" {
		return os.TempDir()
	}
	return g.TempRoot
}

func (g *Generator) ffmpegPath() string {
	if path := g.configuredFFmpegPath(); path != "" {
		return path
	}
	return "ffmpeg"
}

// configuredFFmpegPath is the encoder's location when the deployment named one,
// and empty when it did not. The downloader is given only a path it can use, so
// that an unset value falls back to its own search instead of being handed a
// bare command name to look up relative to the working directory.
func (g *Generator) configuredFFmpegPath() string {
	if g.FFmpegPath == nil {
		return ""
	}
	return g.FFmpegPath()
}

// ytdlpPath resolves the downloader: the configured path first, then an override
// for a deployment that keeps it outside the image, then the path itself.
func (g *Generator) ytdlpPath() string {
	if g.YtDlpPath != "" {
		return g.YtDlpPath
	}
	if override := os.Getenv(YtDlpPathEnv); override != "" {
		return override
	}
	return defaultYtDlpPath
}

const (
	// YtDlpPathEnv overrides where the downloader lives, for a deployment that
	// installs it outside the image.
	YtDlpPathEnv = "SILO_YTDLP_PATH"
	// defaultYtDlpPath is the downloader's name on the path, which is where the
	// image installs it.
	defaultYtDlpPath = "yt-dlp"
	// downloadPrefix is how the downloader names every file it writes: the output
	// name, then the format it came from, as in source.f399.mp4.
	downloadPrefix = "source."
	// youtubeWatchURL is the watch page a site key resolves to.
	youtubeWatchURL = "https://www.youtube.com/watch?v="
	// CommandTimeout bounds one external step, so a stalled download cannot hold
	// a task slot forever.
	CommandTimeout = 10 * time.Minute
)

func runCommand(ctx context.Context, name string, args ...string) error {
	_, err := commandOutput(ctx, name, args...)
	return err
}

func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, CommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", filepath.Base(name), lastLine(message))
	}

	return output, nil
}

// ffprobeOf names the prober beside an encoder: a configured encoder path is the
// only hint this process has about where the tools were installed.
func ffprobeOf(ffmpeg string) string {
	if strings.HasSuffix(ffmpeg, "ffmpeg") {
		return strings.TrimSuffix(ffmpeg, "ffmpeg") + "ffprobe"
	}
	return "ffprobe"
}

// lastLine keeps failure text to what names the problem: the row holds it, and
// an encoder's tail can run for pages.
func lastLine(message string) string {
	if index := strings.LastIndex(message, "\n"); index >= 0 {
		return strings.TrimSpace(message[index+1:])
	}
	return message
}

// downloadParts names what the downloader left behind when it did not merge
// them. Each part carries its format id between the output name and the
// extension, so an unmuxed 1080p trailer reads as source.f399.mp4 beside
// source.f251.webm. Nothing is classified or ordered here: ffmpeg picks the
// video and audio streams it needs out of the inputs it is given.
func downloadParts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var parts []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, downloadPrefix) {
			continue
		}
		if incompleteSuffixes[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		parts = append(parts, filepath.Join(dir, name))
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("no downloaded file in %s", dir)
	}

	return parts, nil
}

// incompleteSuffixes mark a transfer that has not finished. Handing one to the
// encoder would turn part of a trailer into a whole clip.
var incompleteSuffixes = map[string]bool{".part": true, ".ytdl": true}
