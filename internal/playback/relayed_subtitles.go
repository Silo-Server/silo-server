package playback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// Text subtitle burn-in of a relayed source. The libass subtitles filter
// opens its file itself, outside FFmpeg's input and its restrictions, so it
// must not open a transcode node's loopback URL. Instead the session fetches
// the track's subtitle bundle from the core (subtitle_bundle.go), which builds
// it from its own copy of the source, and unpacks it into a directory of its
// own under the session's output directory; the filter reads the script there
// (si=0) with the bundle's fonts as its fontsdir. A local source keeps the
// symlink alias (prepareSubtitleFilterInput).
//
// The fetch is the session's, not the request's: a start returns at once with
// a preparing session, which launches FFmpeg once the bundle is unpacked
// (TranscodeSession.preparing), and closing the session cancels the fetch.
// Restarts reuse the session's bundle. The core keeps the bundle cached, so a
// retry, a rebuild or another node asking for the same track gets it without
// a second demux of the source.

const (
	// relayedSubtitleTimeout bounds a session's wait for its bundle: the
	// core's bound on building it, and a minute to fetch it.
	relayedSubtitleTimeout = subtitleBundleBuildTimeout + time.Minute
	// relayedSubtitleDirPrefix names the directories bundles unpack into, in
	// a session's output directory.
	relayedSubtitleDirPrefix = "subtitle-"
)

var (
	// relayedSubtitleGiveUp is how long a session keeps asking a core that
	// does not answer, or answers with a passing failure, before its staging
	// fails as retryable (ErrSubtitleStagingUnavailable).
	relayedSubtitleGiveUp = 30 * time.Second
	// relayedSubtitlePoll is the least time between two requests for a
	// bundle. The core holds a request for a bundle it is building for a few
	// seconds before it answers that the bundle is pending.
	relayedSubtitlePoll = time.Second
)

// ErrSubtitleStaging marks a session whose text subtitle could not be staged
// for burn-in. It is the session's WaitError, so a startup does not take it
// for a hardware failure and try another path on the same source.
var ErrSubtitleStaging = errors.New("stage subtitle")

// ErrSubtitleStagingUnavailable is a staging that failed for a passing
// reason: the core could not be reached, failed, or did not build the bundle
// in time. A later attempt may succeed.
var ErrSubtitleStagingUnavailable = fmt.Errorf("%w: the core could not provide the subtitle", ErrSubtitleStaging)

// textSubtitleBurnIn reports a session that burns in a text subtitle through
// the libass subtitles filter. Bitmap subtitles overlay the decoded stream.
func textSubtitleBurnIn(opts TranscodeOpts) bool {
	return opts.SubtitleBurnIn && opts.SubtitleTrackIndex >= 0 && !NeedsBurnIn(opts.SubtitleCodec)
}

// stagesRelayedSubtitle reports a text burn-in of a relayed source, whose
// track must be staged before FFmpeg starts. Copied video renders no
// subtitle, so it stages nothing.
func stagesRelayedSubtitle(opts TranscodeOpts) bool {
	return textSubtitleBurnIn(opts) && !strings.EqualFold(opts.TargetCodecVideo, "copy") && nodesource.IsRemote(opts.InputPath)
}

// stagedSubtitle is a session's fetch of its subtitle bundle. done closes
// when the fetch has ended, with dir, the unpacked bundle, or err set.
type stagedSubtitle struct {
	done   chan struct{}
	cancel context.CancelFunc
	dir    string
	err    error
}

// ended reports a staging that has ended.
func (st *stagedSubtitle) ended() bool {
	select {
	case <-st.done:
		return true
	default:
		return false
	}
}

// ready reports a staging that completed.
func (st *stagedSubtitle) ready() bool { return st.ended() && st.err == nil }

// failed reports a staging that ended without completing.
func (st *stagedSubtitle) failed() bool { return st.ended() && st.err != nil }

// stop cancels the staging and waits for it to end, so nothing writes into
// the session's output directory after it returns.
func (st *stagedSubtitle) stop() {
	if st == nil {
		return
	}
	st.cancel()
	<-st.done
}

// stageRelayedSubtitle begins fetching opts' subtitle bundle into a new
// directory under opts.OutputDir. The fetch ends with the source's handle,
// and within relayedSubtitleTimeout.
func stageRelayedSubtitle(opts TranscodeOpts) (*stagedSubtitle, error) {
	h, ok := nodesource.Lookup(opts.InputPath)
	if !ok {
		return nil, fmt.Errorf("%w: the source is not relayed", ErrSubtitleStaging)
	}
	ctx, cancel := context.WithTimeout(h.Context(), relayedSubtitleTimeout)
	st := &stagedSubtitle{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer cancel()
		st.dir, st.err = fetchSubtitleBundle(ctx, h, opts)
		close(st.done)
	}()
	return st, nil
}

// fetchSubtitleBundle asks the core for opts' bundle until it has it, and
// unpacks it. A bundle the core is still building is asked for again; a
// passing failure is retried for relayedSubtitleGiveUp.
func fetchSubtitleBundle(ctx context.Context, h *nodesource.Handle, opts TranscodeOpts) (string, error) {
	started := time.Now()
	var failingSince time.Time
	for {
		asked := time.Now()
		body, err := h.FetchSubtitle(ctx, opts.SubtitleTrackIndex, strings.ToLower(opts.SubtitleCodec))
		if err == nil {
			// A download cut short fails the unpack: it passes like any
			// failure to reach the core, as would a bundle the core sent
			// malformed, once the session gives up on it.
			var dir string
			dir, err = unpackRelayedSubtitle(body, opts.OutputDir)
			_ = body.Close()
			if err == nil {
				slog.InfoContext(ctx, "relayed subtitle staged", "component", "playback",
					"playback_session_id", opts.SessionID, "source", h.Identity(), "track", opts.SubtitleTrackIndex,
					"elapsed_ms", time.Since(started).Milliseconds())
				return dir, nil
			}
		}
		var status *nodesource.StatusError
		switch {
		case ctx.Err() != nil:
			if cause := h.Err(); cause != nil {
				return "", fmt.Errorf("%w: %w", ErrSubtitleStaging, cause)
			}
			return "", fmt.Errorf("%w: %w", ErrSubtitleStagingUnavailable, ctx.Err())
		case errors.Is(err, nodesource.ErrSubtitlePending):
			failingSince = time.Time{}
		case errors.As(err, &status) && (status.Fatal() || status.Status == http.StatusUnprocessableEntity):
			// The core refused the source, or cannot build its bundle.
			return "", fmt.Errorf("%w: %w", ErrSubtitleStaging, err)
		case failingSince.IsZero():
			failingSince = asked
		case time.Since(failingSince) >= relayedSubtitleGiveUp:
			return "", fmt.Errorf("%w: %w", ErrSubtitleStagingUnavailable, err)
		}
		select {
		case <-time.After(time.Until(asked.Add(relayedSubtitlePoll))):
		case <-ctx.Done():
		}
	}
}

// unpackRelayedSubtitle unpacks the bundle read from body into a new
// directory under outputDir, which it removes when the bundle is invalid.
func unpackRelayedSubtitle(body io.Reader, outputDir string) (string, error) {
	dir, err := os.MkdirTemp(outputDir, relayedSubtitleDirPrefix)
	if err != nil {
		return "", err
	}
	if err := unpackSubtitleBundle(io.LimitReader(body, maxSubtitleBundleBytes), dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// useStagedSubtitle points opts' subtitles filter at the bundle unpacked in
// dir.
func useStagedSubtitle(opts *TranscodeOpts, dir string) {
	opts.subtitleFilterInputPath = filepath.Join(dir, subtitleBundleScript)
	opts.subtitleFilterFontsDir = filepath.Join(dir, subtitleBundleFontsDir)
}
