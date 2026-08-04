package handlers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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
	// virtualProbeBudgetKnown caps ffprobe time when the candidate already
	// declares codec and resolution metadata (provider-parsed, not guessed).
	virtualProbeBudgetKnown = 8 * time.Second
	virtualStartupBudget       = 45 * time.Second
	// virtualProbeBudget caps how long a single ffprobe on a virtual/remote
	// stream may take. Large remote sources (e.g. 2160p remuxes behind an
	// origin proxy) can be slow to open and to present media headers, so the
	// budget is deliberately larger than the local-file probe window.
	virtualProbeBudget = 30 * time.Second
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

	// Resolve the best candidate immediately. While it probes, resolve the
	// remaining candidates in the background as failover insurance. This
	// avoids hitting the provider N times in the common case where the
	// first candidate works, while saving a round-trip when it doesn't.
	type resolveResult struct {
		index     int
		candidate VirtualPlaybackStream
		ownerID   int
		url       string
		err       error
	}
	// Fire background resolves for candidates[1:] before probing candidate[0].
	bgCtx, bgCancel := context.WithCancel(attemptCtx)
	defer bgCancel()
	resolveResults := make([]resolveResult, len(candidates))
	var bg sync.WaitGroup
	if len(candidates) > 1 {
		bg.Add(len(candidates) - 1)
		for i, c := range candidates[1:] {
			go func(idx int, cand VirtualPlaybackStream) {
				defer bg.Done()
				oid := cand.OwnerInstallationID
				if oid <= 0 {
					oid = file.VirtualOwnerInstallationID
				}
				url, err := h.VirtualPlaybackResolver.ResolveVirtualPlayback(
					bgCtx, cand.URI, userID, profileID, oid,
				)
				resolveResults[idx+1] = resolveResult{
					index: idx + 1, candidate: cand, ownerID: oid, url: url, err: err,
				}
			}(i, c)
		}
	}

	resolveAndProbe := func(i int, cand VirtualPlaybackStream) (*resolvedVirtualPlaybackSource, error) {
		oid := cand.OwnerInstallationID
		if oid <= 0 {
			oid = file.VirtualOwnerInstallationID
		}
		// If results already filled from a background resolve, use it.
		var streamURL string
		var resolveErr error
		if resolveResults[i].url != "" || resolveResults[i].err != nil {
			streamURL = resolveResults[i].url
			resolveErr = resolveResults[i].err
		} else {
			streamURL, resolveErr = h.VirtualPlaybackResolver.ResolveVirtualPlayback(
				attemptCtx, cand.URI, userID, profileID, oid,
			)
		}
		if resolveErr != nil {
			return nil, resolveErr
		}
		transient := *file
		transient.FilePath = cand.URI
		transient.VirtualOwnerInstallationID = oid
		if h.VirtualPlaybackSourceProber == nil {
			return &resolvedVirtualPlaybackSource{
				URL: streamURL, URI: cand.URI, OwnerID: oid, File: &transient,
			}, nil
		}
		// HLS (.m3u8) streams always route through server remux or transcode —
		// ffprobe is wasted work when the candidate already declares codecs.
		if isHLSVirtualStreamURL(streamURL) && cand.hasReliableCodecs() {
			mergeVirtualCandidateTracks(&transient, cand)
			if !transient.HDR && cand.HDR != "" {
				transient.HDR = true
			}
			return &resolvedVirtualPlaybackSource{
				URL: streamURL, URI: cand.URI, OwnerID: oid, File: &transient, ProbeSucceeded: true,
			}, nil
		}
		// Use a tighter probe budget when the candidate already carries explicit
		// codec metadata — ffprobe only needs to confirm, not discover.
		probeBudget := virtualProbeBudget
		if cand.hasReliableCodecs() {
			probeBudget = virtualProbeBudgetKnown
		}
		probeCtx, probeCancel := context.WithTimeout(attemptCtx, probeBudget)
		probed, probeErr := h.VirtualPlaybackSourceProber(probeCtx, streamURL, &transient)
		probeCancel()
		if probeErr != nil || probed == nil {
			// Probe failed — if the candidate declares codecs, use them as a
			// best-effort fallback instead of discarding the stream entirely.
			if cand.hasReliableCodecs() {
				mergeVirtualCandidateTracks(&transient, cand)
				return &resolvedVirtualPlaybackSource{
					URL: streamURL, URI: cand.URI, OwnerID: oid,
					File: &transient, ProbeSucceeded: true,
				}, nil
			}
			if probeErr == nil {
				probeErr = errors.New("virtual stream probe returned no metadata")
			}
			return nil, probeErr
		}
		mergeVirtualCandidateTracks(probed, cand)
		return &resolvedVirtualPlaybackSource{
			URL: streamURL, URI: cand.URI, OwnerID: oid, File: probed, ProbeSucceeded: true,
		}, nil
	}

	var firstResolved *resolvedVirtualPlaybackSource
	var attemptErr error
	for i, candidate := range candidates {
		result, err := resolveAndProbe(i, candidate)
		if err != nil {
			attemptErr = errors.Join(attemptErr, err)
			continue
		}
		if result.ProbeSucceeded || h.VirtualPlaybackSourceProber == nil {
			bgCancel() // cancel background resolves, we're done
			return *result, nil
		}
		if firstResolved == nil {
			copy := *result
			firstResolved = &copy
		}
	}
	if firstResolved != nil {
		if len(candidates) > 0 {
			mergeVirtualCandidateTracks(firstResolved.File, candidates[0])
		}
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
	// Guard against cross-identity candidates: only consider streams that
	// share the same scheme, host, path, and profile as the original file.
	streams = filterVirtualPlaybackStreams(file, streams)
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
	probeCtx, probeCancel := context.WithTimeout(ctx, virtualProbeBudget)
	probed, probeErr := h.VirtualPlaybackSourceProber(probeCtx, streamURL, &transient)
	probeCancel()
	if probeErr != nil || probed == nil {
		return nil, errors.New("virtual stream probe failed during fallback")
	}
	resolved.File = probed
	mergeVirtualCandidateTracks(resolved.File, candidate)
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



// hasReliableCodecs returns true when the candidate carries explicit codec
// metadata (parsed from provider information, not just a filename guess).
// HLS streams with known codecs can skip ffprobe entirely.
func (s VirtualPlaybackStream) hasReliableCodecs() bool {
	return s.CodecVideo != "" && s.Resolution != ""
}
// mergeVirtualCandidateTracks supplements probed virtual file tracks with
// metadata from the provider candidate. ffprobe may not always detect
// language tags, codecs, or dimensions on remote streams (especially HLS
// and DASH), so candidate metadata fills the gaps so virtual files appear
// as close to local files as possible.
func mergeVirtualCandidateTracks(probed *models.MediaFile, candidate VirtualPlaybackStream) {
	if probed == nil {
		return
	}

	// Fill empty top-level fields that ffprobe may miss on remote streams.
	if probed.Resolution == "" {
		probed.Resolution = candidate.Resolution
	}
	if probed.CodecVideo == "" {
		probed.CodecVideo = candidate.CodecVideo
	}
	if probed.CodecAudio == "" {
		probed.CodecAudio = candidate.CodecAudio
	}
	if !probed.HDR && candidate.HDR != "" {
		probed.HDR = true
	}
	if probed.Container == "" {
		probed.Container = candidate.Container
	}
	if probed.FileSize == 0 {
		probed.FileSize = candidate.FileSize
	}
	if probed.Bitrate == 0 {
		probed.Bitrate = candidate.Bitrate
	}

	// Infer audio channels from the codec when ffprobe didn't detect them.
	channels := inferChannelsFromCodec(probed.CodecAudio)

	// Create a basic video track when ffprobe didn't detect any.
	if len(probed.VideoTracks) == 0 && candidate.CodecVideo != "" {
		probed.VideoTracks = append(probed.VideoTracks, models.VideoTrack{
			Codec: candidate.CodecVideo,
			Width: resolutionWidth(candidate.Resolution),
			Height: resolutionHeight(candidate.Resolution),
		})
		if probed.CodecVideo == "" {
			probed.CodecVideo = candidate.CodecVideo
		}
	}

	// Fill audio channels on existing tracks that lack them.
	for i := range probed.AudioTracks {
		if probed.AudioTracks[i].Codec == "" && probed.CodecAudio != "" {
			probed.AudioTracks[i].Codec = probed.CodecAudio
		}
		if probed.AudioTracks[i].Channels == 0 {
			probed.AudioTracks[i].Channels = channels
		}
	}

	// Merge candidate audio languages into tracks.
	if len(candidate.AudioLanguages) > 0 {
		existing := make(map[string]bool, len(probed.AudioTracks))
		for _, t := range probed.AudioTracks {
			if lang := strings.TrimSpace(t.Language); lang != "" {
				existing[strings.ToLower(lang)] = true
			}
		}
		for _, lang := range candidate.AudioLanguages {
			lang = strings.TrimSpace(lang)
			if lang == "" || existing[strings.ToLower(lang)] {
				continue
			}
			existing[strings.ToLower(lang)] = true
			probed.AudioTracks = append(probed.AudioTracks, models.AudioTrack{
				Language: lang,
				Codec:    probed.CodecAudio,
				Channels: channels,
			})
		}
	}

	// Merge candidate subtitle languages into tracks.
	if len(candidate.SubtitleLanguages) > 0 {
		existing := make(map[string]bool, len(probed.SubtitleTracks))
		for _, t := range probed.SubtitleTracks {
			if lang := strings.TrimSpace(t.Language); lang != "" {
				existing[strings.ToLower(lang)] = true
			}
		}
		for i, lang := range candidate.SubtitleLanguages {
			lang = strings.TrimSpace(lang)
			if lang == "" || existing[strings.ToLower(lang)] {
				continue
			}
			existing[strings.ToLower(lang)] = true
			probed.SubtitleTracks = append(probed.SubtitleTracks, models.SubtitleTrack{
				Index:    len(probed.SubtitleTracks) + i,
				Language: lang,
			})
		}
	}
}

// inferChannelsFromCodec returns a plausible channel count for a codec string.
func inferChannelsFromCodec(codec string) int {
	switch strings.ToLower(codec) {
	case "atmos":
		return 8
	case "truehd", "dts-hd", "dts", "eac3", "ac3":
		return 6
	default:
		return 2
	}
}

// resolutionWidth returns a typical width for a resolution label.
func resolutionWidth(label string) int {
	switch strings.ToLower(label) {
	case "2160p":
		return 3840
	case "1080p":
		return 1920
	case "720p":
		return 1280
	case "480p":
		return 720
	default:
		return 0
	}
}

// resolutionHeight returns a typical height for a resolution label.
func resolutionHeight(label string) int {
	switch strings.ToLower(label) {
	case "2160p":
		return 2160
	case "1080p":
		return 1080
	case "720p":
		return 720
	case "480p":
		return 480
	default:
		return 0
	}
}


