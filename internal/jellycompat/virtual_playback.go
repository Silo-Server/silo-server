package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/remotestream"
)

const (
	compatVirtualScheme           = "virtual://"
	compatVirtualListTimeout      = 8 * time.Second
	compatVirtualProbeTimeout     = 10 * time.Second
	compatVirtualStartupTimeout   = 45 * time.Second
	compatVirtualMaxProbeAttempts = 5
)

// VirtualMediaResolver resolves a provider-neutral virtual URI to a temporary
// provider URL. Implementations must keep credentials out of returned errors.
type VirtualMediaResolver interface {
	ResolveVirtualMedia(ctx context.Context, virtualURI string, ownerInstallationID, userID int, profileID string) (string, error)
}

type VirtualMediaResolverFunc func(context.Context, string, int, int, string) (string, error)

func (f VirtualMediaResolverFunc) ResolveVirtualMedia(ctx context.Context, virtualURI string, ownerInstallationID, userID int, profileID string) (string, error) {
	return f(ctx, virtualURI, ownerInstallationID, userID, profileID)
}

// VirtualMediaRefreshResolver bypasses a provider's short-lived result cache.
type VirtualMediaRefreshResolver interface {
	RefreshVirtualMedia(ctx context.Context, virtualURI string, ownerInstallationID, userID int, profileID string) (string, error)
}

type VirtualMediaRefreshResolverFunc func(context.Context, string, int, int, string) (string, error)

func (f VirtualMediaRefreshResolverFunc) RefreshVirtualMedia(ctx context.Context, virtualURI string, ownerInstallationID, userID int, profileID string) (string, error) {
	return f(ctx, virtualURI, ownerInstallationID, userID, profileID)
}

// VirtualPlaybackStream is the provider-neutral portion of a current provider
// result. Temporary provider URLs never cross this interface.
type VirtualPlaybackStream struct {
	URI                 string
	Label               string
	OwnerInstallationID int
}

type VirtualPlaybackStreamLister interface {
	ListVirtualPlaybackStreams(ctx context.Context, virtualURI string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error)
}

type VirtualPlaybackStreamListerFunc func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error)

func (f VirtualPlaybackStreamListerFunc) ListVirtualPlaybackStreams(ctx context.Context, virtualURI string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error) {
	return f(ctx, virtualURI, userID, profileID, ownerInstallationID)
}

type VirtualSourceProber func(context.Context, string, *models.MediaFile) (*models.MediaFile, error)

// RemoteStreamRelay is the credential-hiding, SSRF-protected transport shared
// by direct delivery and FFmpeg inputs.
type RemoteStreamRelay interface {
	Proxy(http.ResponseWriter, *http.Request, string) error
	Register(context.Context, string) (string, func(), error)
}

func isCompatVirtualPath(path string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(path)), compatVirtualScheme)
}

func isCompatVirtualFile(file *models.MediaFile) bool {
	return file != nil && (isCompatVirtualPath(file.FilePath) || strings.EqualFold(strings.TrimSpace(file.Container), "virtual"))
}

func isCompatVirtualSource(source PlaybackMediaSource) bool {
	return isCompatVirtualPath(source.VirtualSourceURI) || isCompatVirtualPath(source.Version.FilePath) || strings.EqualFold(strings.TrimSpace(source.Version.Container), "virtual")
}

// boundVirtualDownloadSource returns the exact provider-neutral source chosen
// during PlaybackInfo. A file-ID fallback covers clients that omit or rewrite
// MediaSourceId on their download/range requests without permitting a source
// from another file to be substituted.
func (h *PlaybackHandler) boundVirtualDownloadSource(session *Session, playSessionID string, fileID int, mediaSourceID string) *PlaybackMediaSource {
	if h == nil || h.playbackStore == nil || session == nil || strings.TrimSpace(playSessionID) == "" {
		return nil
	}
	playSession, ok := h.playbackStore.Get(playSessionID)
	if !ok || playSession.CompatToken != session.Token {
		return nil
	}
	if source := findMediaSource(playSession, mediaSourceID); source != nil && source.FileID == fileID && isCompatVirtualSource(*source) {
		return source
	}
	for i := range playSession.MediaSources {
		source := playSession.MediaSources[i]
		if source.FileID == fileID && isCompatVirtualSource(source) {
			return &source
		}
	}
	return nil
}

type resolvedCompatVirtualSource struct {
	file    *models.MediaFile
	uri     string
	ownerID int
}

func (h *PlaybackHandler) prepareVirtualPlaybackVersion(ctx context.Context, session *Session, version catalog.FileVersion) (catalog.FileVersion, string, int, error) {
	if !isCompatVirtualPath(version.FilePath) && !strings.EqualFold(strings.TrimSpace(version.Container), "virtual") {
		return version, "", 0, nil
	}
	if session == nil || h.fileResolver == nil {
		return version, "", 0, errors.New("virtual playback dependencies are unavailable")
	}
	file, err := h.fileResolver.GetByID(ctx, version.FileID)
	if err != nil || file == nil {
		return version, "", 0, errors.New("virtual media file is unavailable")
	}
	resolved, err := h.resolveAndProbeVirtualSource(ctx, file, session.StreamAppUserID, session.ProfileID)
	if err != nil {
		return version, "", 0, err
	}
	return applyVirtualProbeToVersion(version, resolved.file), resolved.uri, resolved.ownerID, nil
}

func (h *PlaybackHandler) resolveAndProbeVirtualSource(ctx context.Context, file *models.MediaFile, userID int, profileID string) (resolvedCompatVirtualSource, error) {
	if !isCompatVirtualFile(file) {
		return resolvedCompatVirtualSource{file: file}, nil
	}
	if h.VirtualMediaResolver == nil || h.VirtualSourceProber == nil {
		return resolvedCompatVirtualSource{}, errors.New("virtual playback resolution is not configured")
	}

	candidates := []VirtualPlaybackStream{{URI: file.FilePath, OwnerInstallationID: file.VirtualOwnerInstallationID}}
	parsed, _ := url.Parse(file.FilePath)
	if parsed != nil && strings.TrimSpace(parsed.Query().Get("result")) == "" && h.VirtualPlaybackStreamLister != nil {
		listCtx, cancel := context.WithTimeout(ctx, compatVirtualListTimeout)
		streams, err := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(listCtx, file.FilePath, userID, profileID, file.VirtualOwnerInstallationID)
		cancel()
		if err == nil {
			if filtered := filterCompatVirtualCandidates(file.FilePath, streams); len(filtered) > 0 {
				candidates = filtered
			}
		}
	}
	if len(candidates) > compatVirtualMaxProbeAttempts {
		candidates = candidates[:compatVirtualMaxProbeAttempts]
	}

	attemptCtx, cancel := context.WithTimeout(ctx, compatVirtualStartupTimeout)
	defer cancel()
	var attemptErr error
	for _, candidate := range candidates {
		uri := strings.TrimSpace(candidate.URI)
		if !isCompatVirtualPath(uri) {
			continue
		}
		ownerID := candidate.OwnerInstallationID
		if ownerID <= 0 {
			ownerID = file.VirtualOwnerInstallationID
		}
		resolvedURL, err := h.VirtualMediaResolver.ResolveVirtualMedia(attemptCtx, uri, ownerID, userID, profileID)
		if err != nil {
			attemptErr = errors.Join(attemptErr, err)
			continue
		}
		transient := *file
		transient.FilePath = uri
		transient.VirtualOwnerInstallationID = ownerID
		probeCtx, probeCancel := context.WithTimeout(attemptCtx, compatVirtualProbeTimeout)
		probed, err := h.VirtualSourceProber(probeCtx, resolvedURL, &transient)
		probeCancel()
		if err != nil || probed == nil {
			attemptErr = errors.Join(attemptErr, err)
			continue
		}
		probed.FilePath = uri
		probed.VirtualOwnerInstallationID = ownerID
		return resolvedCompatVirtualSource{file: probed, uri: uri, ownerID: ownerID}, nil
	}
	if attemptErr == nil {
		attemptErr = errors.New("virtual playback provider returned no usable stream")
	}
	return resolvedCompatVirtualSource{}, attemptErr
}

func filterCompatVirtualCandidates(baseURI string, streams []VirtualPlaybackStream) []VirtualPlaybackStream {
	base, err := url.Parse(baseURI)
	if err != nil {
		return nil
	}
	profile := strings.TrimSpace(base.Query().Get("profile"))
	result := strings.TrimSpace(base.Query().Get("result"))
	seen := make(map[string]struct{}, len(streams))
	out := make([]VirtualPlaybackStream, 0, len(streams))
	for _, stream := range streams {
		if len(out) >= compatVirtualMaxProbeAttempts {
			break
		}
		uri := strings.TrimSpace(stream.URI)
		if !isCompatVirtualPath(uri) {
			continue
		}
		parsed, parseErr := url.Parse(uri)
		if parseErr != nil || !sameCompatVirtualIdentity(base, parsed) {
			continue
		}
		query := parsed.Query()
		if result != "" && query.Get("result") != result {
			continue
		}
		if profile != "" && !strings.EqualFold(strings.TrimSpace(stream.Label), profile) && !strings.EqualFold(strings.TrimSpace(query.Get("profile")), profile) {
			continue
		}
		key := strings.ToLower(uri)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, stream)
	}
	return out
}

func sameCompatVirtualIdentity(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host) && a.EscapedPath() == b.EscapedPath()
}

func applyVirtualProbeToVersion(version catalog.FileVersion, file *models.MediaFile) catalog.FileVersion {
	if file == nil {
		return version
	}
	version.FilePath = file.FilePath
	version.Resolution = file.Resolution
	version.CodecVideo = file.CodecVideo
	version.CodecAudio = file.CodecAudio
	version.HDR = file.HDR
	version.Container = file.Container
	version.FileSize = file.FileSize
	version.Duration = file.Duration
	version.Bitrate = file.Bitrate
	version.VideoTracks = append([]models.VideoTrack(nil), file.VideoTracks...)
	version.AudioTracks = append([]models.AudioTrack(nil), file.AudioTracks...)
	version.SubtitleTracks = make([]catalog.VersionSubtitleTrack, 0, len(file.SubtitleTracks))
	for _, track := range file.SubtitleTracks {
		version.SubtitleTracks = append(version.SubtitleTracks, catalog.VersionSubtitleTrack{
			Index: track.Index, Language: track.Language, Codec: track.Codec,
			Title: track.Title, EmbeddedTitle: track.EmbeddedTitle, Resolution: track.Resolution,
			Forced: track.Forced, Default: track.Default, HearingImpaired: track.HearingImpaired,
			External: track.External, FileName: track.FileName,
		})
	}
	return version
}

func virtualMediaFileForSource(file *models.MediaFile, source PlaybackMediaSource) *models.MediaFile {
	if file == nil || !isCompatVirtualSource(source) {
		return file
	}
	copy := *file
	copy.FilePath = source.VirtualSourceURI
	copy.VirtualOwnerInstallationID = source.VirtualSourceOwnerInstallationID
	copy.Resolution = source.Version.Resolution
	copy.CodecVideo = source.Version.CodecVideo
	copy.CodecAudio = source.Version.CodecAudio
	copy.HDR = source.Version.HDR
	copy.Container = source.Version.Container
	copy.FileSize = source.Version.FileSize
	copy.Duration = source.Version.Duration
	copy.Bitrate = source.Version.Bitrate
	copy.VideoTracks = append([]models.VideoTrack(nil), source.Version.VideoTracks...)
	copy.AudioTracks = append([]models.AudioTrack(nil), source.Version.AudioTracks...)
	copy.SubtitleTracks = make([]models.SubtitleTrack, 0, len(source.Version.SubtitleTracks))
	for _, track := range source.Version.SubtitleTracks {
		if track.External {
			continue
		}
		copy.SubtitleTracks = append(copy.SubtitleTracks, models.SubtitleTrack{
			Index: track.Index, Language: track.Language, Codec: track.Codec,
			Title: track.Title, EmbeddedTitle: track.EmbeddedTitle, Resolution: track.Resolution,
			Forced: track.Forced, Default: track.Default, HearingImpaired: track.HearingImpaired,
		})
	}
	return &copy
}

func (h *PlaybackHandler) resolveVirtualTransport(ctx context.Context, session *Session, source PlaybackMediaSource, forceRefresh bool) (string, error) {
	if session == nil {
		return "", errors.New("compat playback session is unavailable")
	}
	return h.resolveVirtualTransportForIdentity(ctx, session.StreamAppUserID, session.ProfileID, source, forceRefresh)
}

func (h *PlaybackHandler) resolveVirtualTransportForIdentity(ctx context.Context, userID int, profileID string, source PlaybackMediaSource, forceRefresh bool) (string, error) {
	uri := strings.TrimSpace(source.VirtualSourceURI)
	if !isCompatVirtualPath(uri) {
		return "", errors.New("virtual playback source is not bound")
	}
	if h.VirtualMediaResolver == nil {
		return "", errors.New("virtual playback resolver is not configured")
	}
	if forceRefresh && h.VirtualMediaRefreshResolver != nil {
		return h.VirtualMediaRefreshResolver.RefreshVirtualMedia(ctx, uri, source.VirtualSourceOwnerInstallationID, userID, profileID)
	}
	return h.VirtualMediaResolver.ResolveVirtualMedia(ctx, uri, source.VirtualSourceOwnerInstallationID, userID, profileID)
}

func (h *PlaybackHandler) registerVirtualInput(ctx context.Context, session *Session, source PlaybackMediaSource, forceRefresh bool) (string, func(), error) {
	if session == nil {
		return "", nil, errors.New("compat playback session is unavailable")
	}
	return h.registerVirtualInputForIdentity(ctx, session.StreamAppUserID, session.ProfileID, source, forceRefresh)
}

func (h *PlaybackHandler) registerVirtualInputForIdentity(ctx context.Context, userID int, profileID string, source PlaybackMediaSource, forceRefresh bool) (string, func(), error) {
	resolved, err := h.resolveVirtualTransportForIdentity(ctx, userID, profileID, source, forceRefresh)
	if err != nil {
		return "", nil, err
	}
	// Feed FFmpeg the resolved provider URL directly instead of routing it
	// through the loopback relay (see playback regression on 2026-08-01).
	return resolved, nil, nil
}

func (h *PlaybackHandler) serveVirtualDirect(w http.ResponseWriter, r *http.Request, session *Session, source PlaybackMediaSource) error {
	if h.RemoteStreamRelay == nil {
		return errors.New("remote stream relay is not configured")
	}
	resolved, err := h.resolveVirtualTransport(r.Context(), session, source, false)
	if err != nil {
		return err
	}
	streamWriter := httpstream.NewRollingDeadlineWriter(w)
	err = h.RemoteStreamRelay.Proxy(streamWriter, r, resolved)
	var proxyErr *remotestream.ProxyError
	if errors.As(err, &proxyErr) && proxyErr.Started {
		// Headers or media bytes already reached the client. The connection error
		// itself is the response; appending a JSON error would corrupt the stream.
		return nil
	}
	if err == nil || !remotestream.RetryableBeforeResponse(err) || h.VirtualMediaRefreshResolver == nil {
		return err
	}
	resolved, refreshErr := h.resolveVirtualTransport(r.Context(), session, source, true)
	if refreshErr != nil {
		return refreshErr
	}
	err = h.RemoteStreamRelay.Proxy(streamWriter, r, resolved)
	if errors.As(err, &proxyErr) && proxyErr.Started {
		return nil
	}
	return err
}

func virtualDownloadName(version catalog.FileVersion) string {
	name := strings.TrimSpace(version.FileName)
	if name == "" {
		name = "stream"
	}
	if filepath.Ext(name) == "" && strings.TrimSpace(version.Container) != "" {
		name += "." + strings.ToLower(strings.TrimSpace(version.Container))
	}
	return name
}
