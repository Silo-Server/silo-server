package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const virtualPlaybackPrefix = "virtual://"

const maxVirtualPlaybackStreams = 50

const (
	maxVirtualFailoverAttempts = 5
	virtualStartupBudget       = 45 * time.Second
)

type VirtualPlaybackResolver interface {
	ResolveVirtualPlayback(ctx context.Context, virtualPath string, userID int, profileID string, ownerInstallationID int) (string, error)
}

type VirtualPlaybackResolverFunc func(context.Context, string, int, string, int) (string, error)

func (f VirtualPlaybackResolverFunc) ResolveVirtualPlayback(ctx context.Context, path string, userID int, profileID string, ownerInstallationID int) (string, error) {
	return f(ctx, path, userID, profileID, ownerInstallationID)
}

// VirtualPlaybackStream is the provider-neutral candidate shape used by the
// just-in-time picker. Implementations must never expose provider URLs here.
type VirtualPlaybackStream struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	URI                 string   `json:"uri"`
	Resolution          string   `json:"resolution,omitempty"`
	CodecVideo          string   `json:"codec_video,omitempty"`
	CodecAudio          string   `json:"codec_audio,omitempty"`
	HDR                 string   `json:"hdr,omitempty"`
	SourceType          string   `json:"source_type,omitempty"`
	FileSize            int64    `json:"file_size,omitempty"`
	Container           string   `json:"container,omitempty"`
	Bitrate             int      `json:"bitrate,omitempty"`
	AudioLanguages      []string `json:"audio_languages,omitempty"`
	SubtitleLanguages   []string `json:"subtitle_languages,omitempty"`
	OwnerInstallationID int      `json:"-"`
}

type VirtualPlaybackStreamLister interface {
	ListVirtualPlaybackStreams(ctx context.Context, virtualPath string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error)
}

type VirtualPlaybackStreamListerFunc func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error)

func (f VirtualPlaybackStreamListerFunc) ListVirtualPlaybackStreams(ctx context.Context, path string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error) {
	return f(ctx, path, userID, profileID, ownerInstallationID)
}

// VirtualPlaybackStreamSink persists JIT candidates as selectable virtual
// files.
type VirtualPlaybackStreamSink func(context.Context, *models.MediaFile, []VirtualPlaybackStream) error

type resolvedVirtualPlaybackSource struct {
	URL            string
	URI            string
	OwnerID        int
	File           *models.MediaFile
	ProbeSucceeded bool
}

// resolveVirtualPlaybackSource chooses a ranked provider-neutral result,
// resolves it, and probes it before planning. A result URI is bound to the
// session so later Range, seek, subtitle, and transcode requests cannot silently
// switch to a technically different candidate under the original plan.
func (h *PlaybackHandler) resolveVirtualPlaybackSource(r *http.Request, file *models.MediaFile, profileID string) (resolvedVirtualPlaybackSource, error) {
	if !isVirtualPlaybackFile(file) {
		return resolvedVirtualPlaybackSource{File: file}, nil
	}
	if h.VirtualPlaybackResolver == nil {
		return resolvedVirtualPlaybackSource{}, errors.New("virtual playback resolver is not configured")
	}
	userID := apimw.GetUserID(r.Context())
	candidates := []VirtualPlaybackStream{{
		URI: file.FilePath, OwnerInstallationID: file.VirtualOwnerInstallationID,
	}}
	parsed, _ := url.Parse(file.FilePath)
	if parsed != nil && strings.TrimSpace(parsed.Query().Get("result")) == "" && h.VirtualPlaybackStreamLister != nil {
		listCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		streams, err := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(
			listCtx, file.FilePath, userID, profileID, file.VirtualOwnerInstallationID,
		)
		if err == nil && len(streams) > 0 {
			if len(streams) > maxVirtualPlaybackStreams {
				streams = streams[:maxVirtualPlaybackStreams]
			}
			if h.VirtualPlaybackStreamSink != nil {
				_ = h.VirtualPlaybackStreamSink(listCtx, file, streams)
			}
			if filtered := filterVirtualPlaybackStreams(file, streams); len(filtered) > 0 {
				candidates = filtered
			}
		}
		cancel()
	}
	if len(candidates) > maxVirtualFailoverAttempts {
		candidates = candidates[:maxVirtualFailoverAttempts]
	}
	attemptCtx, cancel := context.WithTimeout(r.Context(), virtualStartupBudget)
	defer cancel()
	var firstResolved *resolvedVirtualPlaybackSource
	var attemptErr error
	for _, candidate := range candidates {
		ownerID := candidate.OwnerInstallationID
		if ownerID <= 0 {
			ownerID = file.VirtualOwnerInstallationID
		}
		streamURL, err := h.VirtualPlaybackResolver.ResolveVirtualPlayback(
			attemptCtx, candidate.URI, userID, profileID, ownerID,
		)
		if err != nil {
			attemptErr = errors.Join(attemptErr, err)
			continue
		}
		transient := *file
		transient.FilePath = candidate.URI
		transient.VirtualOwnerInstallationID = ownerID
		resolved := resolvedVirtualPlaybackSource{
			URL: streamURL, URI: candidate.URI, OwnerID: ownerID, File: &transient,
		}
		if firstResolved == nil {
			copy := resolved
			firstResolved = &copy
		}
		if h.VirtualPlaybackSourceProber == nil {
			return resolved, nil
		}
		probeCtx, probeCancel := context.WithTimeout(attemptCtx, 10*time.Second)
		probed, probeErr := h.VirtualPlaybackSourceProber(probeCtx, streamURL, &transient)
		probeCancel()
		if probeErr != nil || probed == nil {
			if probeErr == nil {
				probeErr = errors.New("virtual stream probe returned no metadata")
			}
			attemptErr = errors.Join(attemptErr, probeErr)
			continue
		}
		resolved.File = probed
		resolved.ProbeSucceeded = true
		return resolved, nil
	}
	if firstResolved != nil {
		// Unknown technical metadata routes through the conservative H264/AAC
		// planner fallback; retaining the first resolved URI is safer than
		// guessing direct-play compatibility.
		return *firstResolved, nil
	}
	if attemptErr == nil {
		attemptErr = errors.New("virtual playback provider returned no usable stream")
	}
	// When the primary resolution fails — commonly because a previously
	// persisted "result=" candidate has rotated or expired at the provider —
	// re-rank the current provider candidates provider-neutrally and retry
	// before failing. This keeps one stale indexer/debrid result from turning
	// a still-streamable item into a hard playback failure, without crossing
	// the user's selected quality when a same-profile candidate exists.
	if fb := h.fallbackResolveStaleVirtualSource(attemptCtx, file, userID, profileID); fb != nil {
		return *fb, nil
	}
	return resolvedVirtualPlaybackSource{}, attemptErr
}

// fallbackResolveStaleVirtualSource re-lists the provider's current candidates
// and resolves the first healthy provider-neutral stream. It returns nil when
// the original URI carried no stale result= pick, or when no substitute
// candidate can be resolved, so the caller preserves its original error.
func (h *PlaybackHandler) fallbackResolveStaleVirtualSource(
	ctx context.Context,
	file *models.MediaFile,
	userID int,
	profileID string,
) *resolvedVirtualPlaybackSource {
	parsed, _ := url.Parse(file.FilePath)
	if parsed != nil && strings.TrimSpace(parsed.Query().Get("result")) == "" {
		return nil
	}
	if h.VirtualPlaybackStreamLister == nil {
		return nil
	}
	neutralKey := virtualPlaybackNeutralKey(file.FilePath)
	listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	streams, listErr := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(
		listCtx, neutralKey, userID, profileID, file.VirtualOwnerInstallationID,
	)
	cancel()
	if listErr != nil {
		slog.ErrorContext(ctx, "virtual stale fallback: list failed", "component", "api", "neutral_key", neutralKey, "error", listErr)
		return nil
	}
	if len(streams) == 0 {
		slog.ErrorContext(ctx, "virtual stale fallback: no streams listed", "component", "api", "neutral_key", neutralKey)
		return nil
	}
	if len(streams) > maxVirtualPlaybackStreams {
		streams = streams[:maxVirtualPlaybackStreams]
	}
	for _, stream := range streams {
		if stream.URI == "" || stream.URI == file.FilePath {
			continue
		}
		resolved, err := h.resolveVirtualCandidateSource(ctx, file, stream, userID, profileID)
		if err == nil {
			slog.InfoContext(ctx, "virtual stale fallback: resolved substitute", "component", "api", "original", file.FilePath, "substitute", stream.URI)
			return resolved
		}
		slog.ErrorContext(ctx, "virtual stale fallback: candidate failed", "component", "api", "candidate", stream.URI, "error", err)
	}
	return nil
}

// resolveVirtualCandidateSource resolves and probes a single virtual stream
// candidate, returning a fully-probed source on success.
func (h *PlaybackHandler) resolveVirtualCandidateSource(
	ctx context.Context,
	file *models.MediaFile,
	candidate VirtualPlaybackStream,
	userID int,
	profileID string,
) (*resolvedVirtualPlaybackSource, error) {
	ownerID := candidate.OwnerInstallationID
	if ownerID <= 0 {
		ownerID = file.VirtualOwnerInstallationID
	}
	streamURL, err := h.VirtualPlaybackResolver.ResolveVirtualPlayback(
		ctx, candidate.URI, userID, profileID, ownerID,
	)
	if err != nil {
		return nil, err
	}
	transient := *file
	transient.FilePath = candidate.URI
	transient.VirtualOwnerInstallationID = ownerID
	resolved := resolvedVirtualPlaybackSource{URL: streamURL, URI: candidate.URI, OwnerID: ownerID, File: &transient}
	if h.VirtualPlaybackSourceProber == nil {
		return &resolved, nil
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	probed, probeErr := h.VirtualPlaybackSourceProber(probeCtx, streamURL, &transient)
	probeCancel()
	if probeErr != nil || probed == nil {
		return nil, errors.New("virtual stream probe failed during fallback")
	}
	resolved.File = probed
	resolved.ProbeSucceeded = true
	return &resolved, nil
}

func (h *PlaybackHandler) probeVirtualSessionFile(ctx context.Context, file *models.MediaFile, session *playback.Session) (*models.MediaFile, string, error) {
	file = virtualPlaybackSessionFile(file, session)
	if !isVirtualPlaybackFile(file) {
		return file, "", nil
	}
	if h.VirtualMediaResolver == nil || h.VirtualPlaybackSourceProber == nil {
		return file, "", errors.New("virtual playback probing is not configured")
	}
	resolved, err := resolveVirtualMediaPath(
		ctx, h.VirtualMediaResolver, file.FilePath, file.VirtualOwnerInstallationID,
		session.UserID, session.ProfileID,
	)
	if err != nil {
		return file, "", err
	}
	probed, err := h.VirtualPlaybackSourceProber(ctx, resolved, file)
	if err != nil {
		return file, "", err
	}
	return probed, resolved, nil
}

func virtualPlaybackAlternatives(
	ctx context.Context,
	lister VirtualPlaybackStreamLister,
	file *models.MediaFile,
	userID int,
	profileID string,
) []VirtualPlaybackStream {
	if lister == nil || file == nil {
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	streams, err := lister.ListVirtualPlaybackStreams(
		listCtx, file.FilePath, userID, profileID, file.VirtualOwnerInstallationID,
	)
	if err != nil {
		return nil
	}
	if len(streams) > maxVirtualPlaybackStreams {
		streams = streams[:maxVirtualPlaybackStreams]
	}
	return filterVirtualPlaybackStreams(file, streams)
}

func filterVirtualPlaybackStreams(file *models.MediaFile, streams []VirtualPlaybackStream) []VirtualPlaybackStream {
	if file == nil {
		return nil
	}
	base, err := url.Parse(file.FilePath)
	if err != nil {
		return nil
	}
	baseProfile := strings.TrimSpace(base.Query().Get("profile"))
	seen := map[string]struct{}{file.FilePath: {}}
	alternatives := make([]VirtualPlaybackStream, 0, len(streams))
	for _, stream := range streams {
		if len(alternatives) >= maxVirtualPlaybackStreams-1 {
			break
		}
		stream.URI = strings.TrimSpace(stream.URI)
		if !strings.HasPrefix(strings.ToLower(stream.URI), virtualPlaybackPrefix) {
			continue
		}
		candidate, parseErr := url.Parse(stream.URI)
		if parseErr != nil ||
			!strings.EqualFold(candidate.Scheme, base.Scheme) ||
			!strings.EqualFold(candidate.Host, base.Host) ||
			candidate.EscapedPath() != base.EscapedPath() {
			continue
		}
		if baseProfile != "" && !strings.EqualFold(
			strings.TrimSpace(candidate.Query().Get("profile")), baseProfile,
		) {
			continue
		}
		if _, duplicate := seen[stream.URI]; duplicate {
			continue
		}
		seen[stream.URI] = struct{}{}
		alternatives = append(alternatives, stream)
	}
	return alternatives
}

func isVirtualPlaybackFile(file *models.MediaFile) bool {
	return file != nil && strings.HasPrefix(file.FilePath, virtualPlaybackPrefix)
}

// virtualPlaybackNeutralKey returns the virtual URI with any concrete "result="
// pick removed, preserving the scheme/host/path and the profile so a stale
// provider candidate can be re-resolved provider-neutrally within the same
// quality selection.
func virtualPlaybackNeutralKey(virtualPath string) string {
	parsed, err := url.Parse(virtualPath)
	if err != nil {
		return virtualPath
	}
	q := parsed.Query()
	if strings.TrimSpace(q.Get("result")) == "" {
		return virtualPath
	}
	q.Del("result")
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func isHLSVirtualStreamURL(streamURL string) bool {
	u, err := url.Parse(streamURL)
	if err != nil {
		return strings.Contains(strings.ToLower(streamURL), ".m3u8")
	}
	return strings.Contains(strings.ToLower(u.Path), ".m3u8") || strings.Contains(strings.ToLower(u.RawQuery), ".m3u8")
}
