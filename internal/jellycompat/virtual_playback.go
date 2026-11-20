package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/remotestream"
	virtstream "github.com/Silo-Server/silo-server/internal/virtuallibrary/stream"
)

const (
	compatVirtualScheme           = "virtual://"
	compatVirtualListTimeout      = 8 * time.Second
	compatVirtualProbeTimeout     = 10 * time.Second
	compatVirtualStartupTimeout   = 45 * time.Second
	compatVirtualMaxProbeAttempts = 5
)

func cloneHeaderMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ResolvedVirtualMedia represents a resolved virtual stream with provider details,
// temporary URL, candidate identity, and proxy request headers.
type ResolvedVirtualMedia struct {
	URL            string
	URI            string
	CandidateID    string
	RequestHeaders map[string]string
	ExpiresAt      time.Time
	// OwnerID is the plugin installation that served the candidate, stamped by
	// the provider resolver. It is the runtime inheritance for a virtual file
	// whose stored owner is 0 and takes precedence over the file owner for the
	// allow_private_streams decision.
	OwnerID int
	// ProviderVideoHash, ProviderGUID, ProviderReleaseName and
	// ProviderReleaseSize are the resolved candidate's durable identity, in
	// the same tier order as the dedup key. They mirror the native resolver
	// shape so both surfaces can persist the same identity.
	ProviderVideoHash   string
	ProviderGUID        string
	ProviderReleaseName string
	ProviderReleaseSize int64
}

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

type VirtualMediaDetailedResolver interface {
	ResolveVirtualMediaDetailed(ctx context.Context, virtualURI string, ownerInstallationID int, userID int, profileID string, forceRefresh bool, excludedCandidateIDs []string, preferredCandidateID string) (ResolvedVirtualMedia, error)
}

type VirtualMediaDetailedResolverFunc func(ctx context.Context, virtualURI string, ownerInstallationID int, userID int, profileID string, forceRefresh bool, excludedCandidateIDs []string, preferredCandidateID string) (ResolvedVirtualMedia, error)

func (f VirtualMediaDetailedResolverFunc) ResolveVirtualMediaDetailed(ctx context.Context, virtualURI string, ownerInstallationID int, userID int, profileID string, forceRefresh bool, excludedCandidateIDs []string, preferredCandidateID string) (ResolvedVirtualMedia, error) {
	return f(ctx, virtualURI, ownerInstallationID, userID, profileID, forceRefresh, excludedCandidateIDs, preferredCandidateID)
}

// effectiveVirtualOwner prefers the provider-resolved owner over the stored
// file owner so a legacy file owner of 0 cannot mask the installation that
// actually served the candidate.
func effectiveVirtualOwner(resolvedOwner, fileOwner int) int {
	if resolvedOwner > 0 {
		return resolvedOwner
	}
	if fileOwner > 0 {
		return fileOwner
	}
	return 0
}

// VirtualPlaybackStream is the provider-neutral portion of a current provider
// result. Temporary provider URLs never cross this interface.
type VirtualPlaybackStream struct {
	URI                 string   `json:"uri"`
	Label               string   `json:"label"`
	Resolution          string   `json:"resolution,omitempty"`
	CodecVideo          string   `json:"codec_video,omitempty"`
	CodecAudio          string   `json:"codec_audio,omitempty"`
	HDR                 string   `json:"hdr,omitempty"`
	Container           string   `json:"container,omitempty"`
	FileSize            int64    `json:"file_size,omitempty"`
	Bitrate             int      `json:"bitrate,omitempty"`
	AudioLanguages      []string `json:"audio_languages,omitempty"`
	SubtitleLanguages   []string `json:"subtitle_languages,omitempty"`
	OwnerInstallationID int      `json:"-"`
}

type VirtualPlaybackStreamLister interface {
	ListVirtualPlaybackStreams(ctx context.Context, virtualURI string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error)
}

type VirtualPlaybackStreamListerFunc func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error)

func (f VirtualPlaybackStreamListerFunc) ListVirtualPlaybackStreams(ctx context.Context, virtualURI string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error) {
	return f(ctx, virtualURI, userID, profileID, ownerInstallationID)
}

type VirtualSourceProber func(context.Context, string, *models.MediaFile) (*models.MediaFile, error)

// VirtualSourceProberWithHeaders probes a provider URL through the same relay
// credentials the playback transport uses. Provider URLs that require replaying
// the caller's request headers (Referer/Origin/User-Agent) fail the probe when
// those headers are dropped, so the header-aware variant is preferred whenever
// the resolver returns RequestHeaders.
type VirtualSourceProberWithHeaders func(context.Context, string, *models.MediaFile, map[string]string) (*models.MediaFile, error)

// VirtualCandidateFileLookup resolves the catalog row for a concrete virtual
// candidate URI, matching provider-neutral siblings when no exact row exists.
// It mirrors internal/api/handlers.VirtualCandidateFileLookup so jellycompat can
// persist probe metadata against the row it actually belongs to.
type VirtualCandidateFileLookup func(ctx context.Context, path, contentID, episodeID string, ownerInstallationID int) (*models.MediaFile, error)

var ErrVirtualCandidateNotFound = errors.New("virtual candidate not found")

// VirtualFileSaver persists a probed virtual inventory back to the catalog
// row, mirroring the native VirtualFileSaver contract. jellycompat cannot
// import internal/api/handlers (that package imports jellycompat), so both
// sides share models.VirtualFilePersistArgs and cmd/silo binds this to
// handlers.VirtualFileMetadataUpdateSQL with the same 18-arg execution as
// the native wiring.
type VirtualFileSaver func(ctx context.Context, args models.VirtualFilePersistArgs) (int64, error)

// VirtualFileMetadataUpdateResult reports metadata persistence separately
// from identity adoption, mirroring handlers.VirtualFileMetadataUpdateResult
// (which jellycompat cannot import). A successful UPDATE is not adoption when
// the row is collection-owned or a sibling guard retained its existing path.
type VirtualFileMetadataUpdateResult struct {
	RowsAffected    int64
	MetadataUpdated bool
	IdentityAdopted bool
}

// VirtualFileMetadataSaver is VirtualFileSaver with the explicit adoption
// result. Prefer it wherever the caller must distinguish "evidence landed"
// from "the row adopted the requested path".
type VirtualFileMetadataSaver func(ctx context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error)

// RemoteStreamRelay is the credential-hiding, SSRF-protected transport shared
// by direct delivery and FFmpeg inputs.
type RemoteStreamRelay interface {
	Proxy(http.ResponseWriter, *http.Request, string) error
	ProxyInsecure(http.ResponseWriter, *http.Request, string) error
	Register(context.Context, string) (string, func(), error)
}

type insecureRemoteStreamRegistrar interface {
	RegisterInsecure(context.Context, string) (string, func(), error)
}

type headerRemoteStreamRegistrar interface {
	RegisterWithHeaders(context.Context, string, map[string]string) (string, func(), error)
	RegisterInsecureWithHeaders(context.Context, string, map[string]string) (string, func(), error)
}

type headerRemoteStreamProxy interface {
	ProxyWithHeaders(http.ResponseWriter, *http.Request, string, map[string]string) error
	ProxyInsecureWithHeaders(http.ResponseWriter, *http.Request, string, map[string]string) error
}

func registerRemoteStreamInput(ctx context.Context, relay RemoteStreamRelay, resolved string, insecure bool) (string, func(), error) {
	return registerRemoteStreamInputWithHeaders(ctx, relay, resolved, nil, insecure)
}

func registerRemoteStreamInputWithHeaders(ctx context.Context, relay RemoteStreamRelay, resolved string, headers map[string]string, insecure bool) (string, func(), error) {
	if hr, ok := relay.(headerRemoteStreamRegistrar); ok {
		if insecure {
			return hr.RegisterInsecureWithHeaders(ctx, resolved, headers)
		}
		return hr.RegisterWithHeaders(ctx, resolved, headers)
	}
	if insecure {
		if registrar, ok := relay.(insecureRemoteStreamRegistrar); ok {
			return registrar.RegisterInsecure(ctx, resolved)
		}
	}
	return relay.Register(ctx, resolved)
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

// Virtual sources resolve through the integrated server and relay before
// remote transcode dispatch, allowing pooled transcode nodes to serve them.
func shouldUseCompatNodePool(source PlaybackMediaSource, file *models.MediaFile) bool {
	return file != nil || source.Version.FilePath != "" || source.VirtualSourceURI != ""
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

	parsed, _ := url.Parse(file.FilePath)
	hasResult := parsed != nil && strings.TrimSpace(parsed.Query().Get("result")) != ""

	candidates := []VirtualPlaybackStream(nil)
	if hasResult {
		candidates = []VirtualPlaybackStream{{
			URI:                 file.FilePath,
			Resolution:          file.Resolution,
			CodecVideo:          file.CodecVideo,
			CodecAudio:          file.CodecAudio,
			Container:           file.Container,
			HDR:                 mediaFileHDRString(file),
			FileSize:            file.FileSize,
			Bitrate:             file.Bitrate,
			OwnerInstallationID: file.VirtualOwnerInstallationID,
		}}
	} else if h.VirtualPlaybackStreamLister != nil {
		listCtx, cancel := context.WithTimeout(ctx, compatVirtualListTimeout)
		streams, err := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(listCtx, file.FilePath, userID, profileID, file.VirtualOwnerInstallationID)
		cancel()
		if err != nil {
			return resolvedCompatVirtualSource{}, fmt.Errorf("list virtual playback streams: %w", err)
		}
		if filtered := filterCompatVirtualCandidates(file.FilePath, streams); len(filtered) > 0 {
			candidates = filtered
		}
	} else {
		candidates = []VirtualPlaybackStream{{
			URI:                 file.FilePath,
			Resolution:          file.Resolution,
			CodecVideo:          file.CodecVideo,
			CodecAudio:          file.CodecAudio,
			Container:           file.Container,
			HDR:                 mediaFileHDRString(file),
			FileSize:            file.FileSize,
			Bitrate:             file.Bitrate,
			OwnerInstallationID: file.VirtualOwnerInstallationID,
		}}
	}
	if len(candidates) > compatVirtualMaxProbeAttempts {
		candidates = candidates[:compatVirtualMaxProbeAttempts]
	}

	for _, candidate := range candidates {
		uri := strings.TrimSpace(candidate.URI)
		if !isCompatVirtualPath(uri) {
			continue
		}
		ownerID := candidate.OwnerInstallationID
		if ownerID <= 0 {
			ownerID = file.VirtualOwnerInstallationID
		}
		transient := *file
		transient.FilePath = uri
		transient.VirtualOwnerInstallationID = ownerID
		if candidate.Resolution != "" {
			transient.Resolution = candidate.Resolution
		}
		if candidate.CodecVideo != "" {
			transient.CodecVideo = candidate.CodecVideo
		}
		if candidate.CodecAudio != "" {
			transient.CodecAudio = candidate.CodecAudio
		}
		if candidate.Container != "" {
			transient.Container = candidate.Container
		}
		if candidate.HDR != "" {
			transient.HDR = true
		}
		if candidate.FileSize > 0 {
			transient.FileSize = candidate.FileSize
		}
		if candidate.Bitrate > 0 {
			transient.Bitrate = candidate.Bitrate
		}

		// A row that has never been probed must run the real probe even when the
		// provider-declared candidate metadata looks complete: declared values
		// are not ground truth, and only a successful probe stamps
		// probe_updated_at so later plays can take the fast candidate-merge
		// path. Already-probed rows skip this entirely (no probe-per-play),
		// matching the native probe gate.
		if file.ProbeUpdatedAt == nil && (h.VirtualSourceProber != nil || h.VirtualSourceProberWithHeaders != nil) {
			probed, probeFailure := h.probeCompatVirtualSource(ctx, &transient, uri, ownerID, userID, profileID)
			if probeFailure == nil {
				if transient.ID > 0 {
					probed.ID = transient.ID
					probed.MediaFolderID = transient.MediaFolderID
				}
				if transient.Duration > 0 && probed.Duration <= 0 {
					probed.Duration = transient.Duration
				}
				mergeCompatCandidateTracks(probed, candidate)
				h.persistCompatVirtualMetadata(ctx, probed, file, uri)
				return resolvedCompatVirtualSource{file: probed, uri: uri, ownerID: ownerID}, nil
			}
			// Resolution or probe failed. Fall back to the candidate-declared
			// metadata exactly like the native path: a probe must never fail the
			// request or block playback. It says so, though: the fallback serves
			// declared values in place of a probe, which is why two clients can
			// see different track counts for the same title while one of them
			// reported the failure and the other did not.
			slog.WarnContext(ctx, "jellycompat virtual source probe failed; serving declared candidate metadata",
				"component", "jellycompat",
				"file_id", file.ID,
				"candidate_uri", uri,
				"reason", probeFailure.Error())
		}

		mergeCompatCandidateTracks(&transient, candidate)
		return resolvedCompatVirtualSource{file: &transient, uri: uri, ownerID: ownerID}, nil
	}
	return resolvedCompatVirtualSource{}, errors.New("virtual playback provider returned no usable stream")
}

// probeCompatVirtualSource resolves the candidate to a temporary provider URL
// and probes that stream for real metadata. It mirrors the native path's
// resolve-then-probe ordering (native probeVirtualSource). A nil error means the
// probe established the file's real inventory; a non-nil one names why it did
// not (resolve failed, probe failed, probe timed out) so the caller's fallback
// to declared values can say so instead of looking like a layout disagreement.
func (h *PlaybackHandler) probeCompatVirtualSource(ctx context.Context, transient *models.MediaFile, uri string, ownerID, userID int, profileID string) (*models.MediaFile, error) {
	if h == nil || (h.VirtualSourceProber == nil && h.VirtualSourceProberWithHeaders == nil) || transient == nil || !isCompatVirtualPath(uri) {
		return nil, errCompatProbeUnavailable
	}
	resolved, err := h.resolveVirtualTransportForIdentity(ctx, userID, profileID, PlaybackMediaSource{
		FileID:                           transient.ID,
		VirtualSourceURI:                 uri,
		VirtualSourceOwnerInstallationID: ownerID,
	}, false)
	if err != nil {
		return nil, fmt.Errorf("resolve failed: %w", err)
	}
	if strings.TrimSpace(resolved.URL) == "" {
		return nil, fmt.Errorf("resolve failed: %w", errCompatProbeEmptyURL)
	}
	probeCtx, cancel := context.WithTimeout(ctx, compatVirtualProbeTimeout)
	defer cancel()
	probed, err := h.probeVirtualSourceWithHeaders(probeCtx, resolved.URL, transient, resolved.RequestHeaders)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || probeCtx.Err() != nil {
			return nil, fmt.Errorf("probe timed out after %s: %w", compatVirtualProbeTimeout, err)
		}
		return nil, fmt.Errorf("probe failed: %w", err)
	}
	if probed == nil {
		return nil, fmt.Errorf("probe failed: %w", errCompatProbeNoResult)
	}
	return probed, nil
}

// The reasons probeCompatVirtualSource reports when it cannot establish a
// probed inventory. They exist so the fallback log names the failure kind rather
// than a bare "unavailable".
var (
	errCompatProbeUnavailable = errors.New("no virtual source prober is wired")
	errCompatProbeEmptyURL    = errors.New("the provider resolved no stream URL")
	errCompatProbeNoResult    = errors.New("the prober returned no file")
)

// probeVirtualSourceWithHeaders prefers the header-aware prober so a probe of a
// provider URL that validates the caller's request headers is authenticated the
// same way the playback relay is. The plain variant stays the fallback for
// nil headers and older embeddings.
func (h *PlaybackHandler) probeVirtualSourceWithHeaders(ctx context.Context, sourceURL string, file *models.MediaFile, headers map[string]string) (*models.MediaFile, error) {
	if h == nil {
		return file, errors.New("playback handler is not configured")
	}
	if h.VirtualSourceProberWithHeaders != nil {
		return h.VirtualSourceProberWithHeaders(ctx, sourceURL, file, headers)
	}
	if h.VirtualSourceProber != nil {
		return h.VirtualSourceProber(ctx, sourceURL, file)
	}
	return file, errors.New("virtual playback source prober is not configured")
}

// compatEvidenceShutdownDrainTimeout bounds how long server shutdown waits
// for in-flight detached compat evidence writes. It is a var so tests can
// shrink the wait.
var compatEvidenceShutdownDrainTimeout = 10 * time.Second

// compatEvidenceMaxConcurrent bounds simultaneous detached compat evidence
// writes server-wide. Beyond the cap the write is dropped and retried by the
// next playback, mirroring the native persist-slot bound.
const compatEvidenceMaxConcurrent = 4

var compatEvidenceSlots = make(chan struct{}, compatEvidenceMaxConcurrent)

func acquireCompatEvidenceSlot() bool {
	select {
	case compatEvidenceSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseCompatEvidenceSlot() {
	select {
	case <-compatEvidenceSlots:
	default:
	}
}

func (h *PlaybackHandler) acquireCompatBackgroundWork() bool {
	if h == nil {
		return false
	}
	h.compatBackgroundMu.Lock()
	defer h.compatBackgroundMu.Unlock()
	if h.compatBackgroundClosed {
		return false
	}
	h.compatBackgroundWG.Add(1)
	return true
}

func (h *PlaybackHandler) releaseCompatBackgroundWork() {
	if h == nil {
		return
	}
	h.compatBackgroundWG.Done()
}

func (h *PlaybackHandler) closeCompatBackgroundWork() {
	if h == nil {
		return
	}
	h.compatBackgroundMu.Lock()
	defer h.compatBackgroundMu.Unlock()
	h.compatBackgroundClosed = true
}

// StartCompatBackgroundShutdownCleanup waits for ctx (the application
// lifecycle) before closing admission, then drains in-flight detached compat
// evidence work within a bounded drain timeout, following the same
// RegisterShutdownWork done-channel contract as the transcode manager
// cleanup. A worker that ignores cancellation only delays shutdown up to the
// drain timeout; the expiry is logged loudly.
func (h *PlaybackHandler) StartCompatBackgroundShutdownCleanup(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if h == nil || ctx == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		<-ctx.Done()
		h.closeCompatBackgroundWork()
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			h.compatBackgroundWG.Wait()
		}()
		timer := time.NewTimer(compatEvidenceShutdownDrainTimeout)
		defer timer.Stop()
		select {
		case <-drained:
		case <-timer.C:
			slog.WarnContext(ctx, "compat background work did not drain before shutdown timeout; proceeding with shutdown",
				"component", "jellycompat", "drain_timeout", compatEvidenceShutdownDrainTimeout)
		}
	}()
	return done
}

// persistCompatVirtualMetadata stamps a successfully probed virtual inventory
// back to the catalog row in the background, mirroring the native
// persistVirtualProbeEvidence pattern. It stamps probe_updated_at through the
// bound SQL so the next play takes the fast candidate-merge path.
//
// The probed transient carries the neutral row's ID but the concrete
// candidate's file_path. The concrete candidate row is resolved first and a
// value snapshot of that row is taken *before* spawning the background
// goroutine, so the CAS fence binds to pre-probe state — not to a lookup
// performed after the probe, and never to a pointer captured across the
// goroutine boundary.
func (h *PlaybackHandler) persistCompatVirtualMetadata(ctx context.Context, file *models.MediaFile, neutral *models.MediaFile, candidateURI string) {
	if h == nil || (h.VirtualFileMetadataSaver == nil && h.VirtualFileSaver == nil) || file == nil || file.ID <= 0 {
		return
	}
	// Snapshot evidence bytes by value now — the caller's `file` pointer is
	// not touched after this point.
	videoJSON := marshalCompatTracks(file.VideoTracks)
	audioJSON := marshalCompatTracks(file.AudioTracks)
	subJSON := marshalCompatTracks(file.SubtitleTracks)
	args := models.VirtualFilePersistArgs{
		VideoTracks:    videoJSON,
		AudioTracks:    audioJSON,
		SubtitleTracks: subJSON,
		Resolution:     file.Resolution,
		CodecVideo:     file.CodecVideo,
		CodecAudio:     file.CodecAudio,
		Container:      file.Container,
		HDR:            file.HDR,
		Bitrate:        file.Bitrate,
		Duration:       file.Duration,
		StampProbe:     true,
	}
	// Synchronously resolve the target row and snapshot its generation
	// *before* leaving this goroutine: the CAS fence needs the pre-probe
	// snapshot, and the background save must not dereference `file` or
	// `neutral` after spawn.
	resolveCtx, resolveCancel := context.WithTimeout(ctx, 3*time.Second)
	target := h.compatVirtualPersistSnapshot(resolveCtx, file, neutral, candidateURI)
	resolveCancel()
	if target.FileID <= 0 {
		return
	}
	args.FileID = target.FileID
	args.ExpectedFilePath = target.ExpectedFilePath
	args.UpdatedAt = target.UpdatedAt
	args.ProbeUpdatedAt = target.ProbeUpdatedAt
	args.OwnerID = target.OwnerID
	args.LibraryID = target.LibraryID
	if !h.acquireCompatBackgroundWork() {
		slog.WarnContext(ctx, "compat virtual metadata persist dropped: background workers closed",
			"component", "jellycompat", "file_id", args.FileID)
		return
	}
	if !acquireCompatEvidenceSlot() {
		h.releaseCompatBackgroundWork()
		slog.WarnContext(ctx, "compat virtual metadata persist dropped: all persist slots busy",
			"component", "jellycompat", "file_id", args.FileID)
		return
	}
	go func(save models.VirtualFilePersistArgs) {
		defer h.releaseCompatBackgroundWork()
		defer releaseCompatEvidenceSlot()
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var updated bool
		var err error
		if h.VirtualFileMetadataSaver != nil {
			var result VirtualFileMetadataUpdateResult
			result, err = h.VirtualFileMetadataSaver(persistCtx, save)
			if err == nil {
				if save.AdoptPath != "" && !result.IdentityAdopted {
					slog.DebugContext(persistCtx, "compat virtual metadata persisted without identity adoption",
						"component", "jellycompat", "file_id", save.FileID)
				}
				updated = result.MetadataUpdated
			}
		} else if h.VirtualFileSaver != nil {
			var rows int64
			rows, err = h.VirtualFileSaver(persistCtx, save)
			updated = err == nil && rows > 0
		} else {
			return
		}
		if err != nil {
			slog.ErrorContext(persistCtx, "compat virtual metadata persist failed", "component", "jellycompat", "file_id", save.FileID, "error", err)
			return
		}
		if !updated {
			slog.DebugContext(persistCtx, "compat virtual metadata persist skipped: stale snapshot", "component", "jellycompat", "file_id", save.FileID)
		}
	}(args)
}

// compatVirtualPersistTarget returns the row id and file_path fence the probe
// inventory belongs to. It prefers the concrete candidate row resolved through
// VirtualCandidateFileLookup; when the lookup is unwired or finds no row it
// falls back to the neutral row's own id and path, which always satisfies the
// bound UPDATE's id+file_path fence.
func (h *PlaybackHandler) compatVirtualPersistTarget(ctx context.Context, probed, neutral *models.MediaFile, candidateURI string) (int, string) {
	snap := h.compatVirtualPersistSnapshot(ctx, probed, neutral, candidateURI)
	return snap.FileID, snap.ExpectedFilePath
}

// compatVirtualPersistSnapshot resolves the target row and snapshots its CAS
// generation in one call, so the background save binds to pre-probe state.
func (h *PlaybackHandler) compatVirtualPersistSnapshot(ctx context.Context, probed, neutral *models.MediaFile, candidateURI string) models.VirtualFilePersistArgs {
	var out models.VirtualFilePersistArgs
	if probed == nil {
		return out
	}
	if neutral == nil {
		out.FileID = probed.ID
		out.ExpectedFilePath = probed.FilePath
		out.UpdatedAt = probed.UpdatedAt
		out.ProbeUpdatedAt = probed.ProbeUpdatedAt
		out.OwnerID = probed.VirtualOwnerInstallationID
		out.LibraryID = probed.MediaFolderID
		return out
	}
	if h.VirtualCandidateFileLookup == nil {
		out.FileID = neutral.ID
		out.ExpectedFilePath = neutral.FilePath
		out.UpdatedAt = neutral.UpdatedAt
		out.ProbeUpdatedAt = neutral.ProbeUpdatedAt
		out.OwnerID = neutral.VirtualOwnerInstallationID
		out.LibraryID = neutral.MediaFolderID
		return out
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	candidate, err := h.VirtualCandidateFileLookup(lookupCtx, compatVirtualNeutralURI(candidateURI), neutral.ContentID, neutral.EpisodeID, neutral.VirtualOwnerInstallationID)
	if err != nil && !errors.Is(err, ErrVirtualCandidateNotFound) {
		slog.WarnContext(ctx, "compat virtual candidate lookup failed; refusing persistence target", "component", "jellycompat", "error", err)
		return out
	}
	if errors.Is(err, ErrVirtualCandidateNotFound) || candidate == nil || candidate.ID <= 0 {
		out.FileID = neutral.ID
		out.ExpectedFilePath = neutral.FilePath
		out.UpdatedAt = neutral.UpdatedAt
		out.ProbeUpdatedAt = neutral.ProbeUpdatedAt
		out.OwnerID = neutral.VirtualOwnerInstallationID
		out.LibraryID = neutral.MediaFolderID
		return out
	}
	out.FileID = candidate.ID
	out.ExpectedFilePath = candidate.FilePath
	out.UpdatedAt = candidate.UpdatedAt
	out.ProbeUpdatedAt = candidate.ProbeUpdatedAt
	out.OwnerID = candidate.VirtualOwnerInstallationID
	out.LibraryID = candidate.MediaFolderID
	return out
}

// marshalCompatTracks renders track slices as JSON arrays, never a bare null,
// so the jsonb array operations in the persistence SQL stay valid.
func marshalCompatTracks(tracks any) []byte {
	if tracks == nil {
		return []byte("[]")
	}
	data, err := json.Marshal(tracks)
	if err != nil || string(data) == "null" {
		return []byte("[]")
	}
	return data
}

func mergeCompatCandidateTracks(probed *models.MediaFile, candidate VirtualPlaybackStream) {
	if probed == nil {
		return
	}
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
	if probed.Container == "" || strings.EqualFold(probed.Container, "virtual") {
		if candidate.Container != "" && !strings.EqualFold(candidate.Container, "virtual") {
			probed.Container = candidate.Container
		} else {
			probed.Container = "mkv"
		}
	}
	if probed.FileSize == 0 {
		probed.FileSize = candidate.FileSize
	}
	if probed.Bitrate == 0 {
		probed.Bitrate = candidate.Bitrate
	}

	channels := inferCompatChannelsFromCodec(probed.CodecAudio)
	videoCodec := probed.CodecVideo
	if videoCodec == "" {
		videoCodec = candidate.CodecVideo
	}
	if videoCodec == "" {
		videoCodec = "h264"
	}
	if probed.CodecVideo == "" {
		probed.CodecVideo = videoCodec
	}
	if len(probed.VideoTracks) == 0 {
		hdrStr := strings.ToLower(candidate.HDR)
		isDV := compatVirtualIsDV(hdrStr)
		dvProfile := compatVirtualDVProfile(candidate.HDR)
		videoRange := "SDR"
		videoRangeType := "SDR"
		dovi := ""
		if isDV {
			videoRange = "DolbyVision"
			videoRangeType = "DOVI"
			if dvProfile == 0 {
				dvProfile = 5
			}
			dovi = "Profile " + strconv.Itoa(dvProfile)
		} else if probed.HDR || candidate.HDR != "" {
			videoRange = "HDR"
			videoRangeType = "HDR10"
		}
		probed.VideoTracks = append(probed.VideoTracks, models.VideoTrack{
			Codec:          videoCodec,
			Width:          compatResolutionWidth(probed.Resolution),
			Height:         compatResolutionHeight(probed.Resolution),
			FrameRate:      "23.976",
			BitDepth:       8,
			Bitrate:        probed.Bitrate,
			VideoRange:     videoRange,
			VideoRangeType: videoRangeType,
			DVProfile:      dvProfile,
			DolbyVision:    dovi,
		})
	}
	for i := range probed.VideoTracks {
		if probed.VideoTracks[i].Width == 0 {
			probed.VideoTracks[i].Width = compatResolutionWidth(probed.Resolution)
		}
		if probed.VideoTracks[i].Height == 0 {
			probed.VideoTracks[i].Height = compatResolutionHeight(probed.Resolution)
		}
		if probed.VideoTracks[i].FrameRate == "" {
			probed.VideoTracks[i].FrameRate = "23.976"
		}
		if probed.VideoTracks[i].BitDepth <= 0 {
			probed.VideoTracks[i].BitDepth = 8
		}
		hdrStr := strings.ToLower(candidate.HDR)
		if compatVirtualIsDV(hdrStr) && probed.VideoTracks[i].DVProfile == 0 {
			probed.VideoTracks[i].VideoRange = "DolbyVision"
			probed.VideoTracks[i].VideoRangeType = "DOVI"
			profile := compatVirtualDVProfile(candidate.HDR)
			if profile == 0 {
				profile = 5
			}
			probed.VideoTracks[i].DVProfile = profile
			probed.VideoTracks[i].DolbyVision = "Profile " + strconv.Itoa(profile)
		} else if !compatVirtualIsDV(hdrStr) && (probed.HDR || candidate.HDR != "") &&
			(strings.TrimSpace(probed.VideoTracks[i].VideoRange) == "" ||
				strings.EqualFold(probed.VideoTracks[i].VideoRange, "sdr") ||
				strings.EqualFold(probed.VideoTracks[i].VideoRangeType, "sdr")) {
			probed.VideoTracks[i].VideoRange = "HDR"
			probed.VideoTracks[i].VideoRangeType = "HDR10"
		}
	}
	audioCodec := probed.CodecAudio
	if audioCodec == "" {
		audioCodec = candidate.CodecAudio
	}
	if audioCodec == "" {
		audioCodec = "aac"
	}
	if probed.CodecAudio == "" {
		probed.CodecAudio = audioCodec
	}
	synthesizeAudio := len(probed.AudioTracks) == 0
	if synthesizeAudio {
		probed.AudioTracks = append(probed.AudioTracks, models.AudioTrack{
			Codec:    audioCodec,
			Channels: channels,
			Default:  true,
		})
	}
	for i := range probed.AudioTracks {
		if probed.AudioTracks[i].Codec == "" {
			probed.AudioTracks[i].Codec = audioCodec
		}
		if probed.AudioTracks[i].Channels == 0 {
			probed.AudioTracks[i].Channels = channels
		}
	}

	if synthesizeAudio && len(candidate.AudioLanguages) > 0 {
		existing := make(map[string]bool, len(candidate.AudioLanguages))
		for _, language := range candidate.AudioLanguages {
			language = strings.TrimSpace(language)
			// Release markers like MULTI/DUAL are not language tags; filter
			// them here exactly like the native surface's
			// isRealVirtualLanguageTag, so a Jellyfin-protocol client gets the
			// same labeled per-language inventory as the native one.
			if language == "" || !isRealCompatLanguageTag(language) {
				continue
			}
			canonical := compatLanguageBaseSubtag(language)
			if existing[canonical] {
				continue
			}
			existing[canonical] = true
			assigned := false
			for i := range probed.AudioTracks {
				if strings.TrimSpace(probed.AudioTracks[i].Language) == "" {
					probed.AudioTracks[i].Language = language
					assigned = true
					break
				}
			}
			if !assigned {
				probed.AudioTracks = append(probed.AudioTracks, models.AudioTrack{
					Language: language,
					Codec:    audioCodec,
					Channels: channels,
				})
			}
		}
	}

	if len(probed.AudioTracks) > 0 {
		hasDefault := false
		for _, t := range probed.AudioTracks {
			if t.Default {
				hasDefault = true
				break
			}
		}
		if !hasDefault {
			probed.AudioTracks[0].Default = true
		}
	}

	if len(candidate.SubtitleLanguages) > 0 {
		// Key by base language (folding regional, bibliographic and display-name
		// aliases), not the lowercase string: a candidate alias ("EN-US") of a
		// probed language ("ENG", "en") must not be appended as a second track
		// for the same language.
		existing := make(map[string]bool, len(probed.SubtitleTracks))
		for _, track := range probed.SubtitleTracks {
			if language := strings.TrimSpace(track.Language); language != "" {
				if base := virtstream.CanonicalLanguageBase(language); base != "" {
					existing[base] = true
				}
			}
		}
		for _, language := range candidate.SubtitleLanguages {
			language = strings.TrimSpace(language)
			if language == "" {
				continue
			}
			base := virtstream.CanonicalLanguageBase(language)
			if base == "" || existing[base] {
				continue
			}
			existing[base] = true
			probed.SubtitleTracks = append(probed.SubtitleTracks, models.SubtitleTrack{
				Index:    len(probed.SubtitleTracks),
				Language: language,
				Codec:    "srt",
				Title:    language,
			})
		}
	}
}

var compatVirtualDVProfileRegex = regexp.MustCompile(`(?i)(?:profile|dovi|dv|dolby\s*vision)\s*[-._:]?\s*0*([1-9]|1\d|20)(?:[.]\d+)?(?:[^a-z0-9]|$)`)

func compatVirtualDVProfile(raw string) int {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if matches := compatVirtualDVProfileRegex.FindStringSubmatch(lower); len(matches) > 1 {
		if profile, err := strconv.Atoi(matches[1]); err == nil && profile > 0 && profile <= 20 {
			return profile
		}
	}
	return 0
}

func compatVirtualIsDV(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.Contains(lower, "dolby vision") || strings.Contains(lower, "dovi") ||
		lower == "dv" || strings.HasPrefix(lower, "dv ") || strings.HasSuffix(lower, " dv") || strings.Contains(lower, " dv ") ||
		compatVirtualDVProfileMarker(lower)
}

func mediaFileHDRString(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	if dvProfile := file.PrimaryDVProfile(); dvProfile > 0 {
		return fmt.Sprintf("Dolby Vision Profile %d", dvProfile)
	}
	if len(file.VideoTracks) > 0 {
		vt := file.VideoTracks[0]
		if vt.DolbyVision != "" {
			return vt.DolbyVision
		}
		if strings.EqualFold(vt.VideoRange, "DolbyVision") || strings.EqualFold(vt.VideoRangeType, "DOVI") {
			return "Dolby Vision"
		}
		if vt.VideoRange != "" && !strings.EqualFold(vt.VideoRange, "SDR") {
			return vt.VideoRange
		}
	}
	if file.HDR {
		return "true"
	}
	return ""
}

// isRealCompatLanguageTag reports whether a provider-declared language token
// parses as a real ISO language subtag. It mirrors the native surface's
// isRealVirtualLanguageTag so release markers like MULTI/DUAL are filtered
// from the synthesized audio inventory on both protocol surfaces.
func isRealCompatLanguageTag(value string) bool {
	tag, err := language.Parse(value)
	if err != nil {
		return false
	}
	base, conf := tag.Base()
	return conf != language.No && base.String() != ""
}

// compatLanguageBaseSubtag canonicalizes a language token to its ISO base
// subtag ("ITA" → "it"), mirroring the native surface's
// virtualLanguageBaseSubtag so probe-recorded codes and provider-declared
// codes dedup against the same key on both protocol surfaces.
func compatLanguageBaseSubtag(value string) string {
	trimmed := strings.TrimSpace(value)
	if tag, err := language.Parse(trimmed); err == nil {
		if base, conf := tag.Base(); conf != language.No && base.String() != "" {
			return base.String()
		}
	}
	return strings.ToLower(trimmed)
}

func compatVirtualDVProfileMarker(raw string) bool {
	for _, profile := range []string{"profile 5", "profile 7", "profile 8", "dv5", "dv7", "dv8"} {
		if strings.Contains(raw, profile) {
			return true
		}
	}
	return false
}

//nolint:goconst
func inferCompatChannelsFromCodec(codec string) int {
	switch strings.ToLower(codec) {
	case "atmos":
		return 8
	case "truehd", "dts-hd", "dts", "eac3", "ac3":
		return 6
	default:
		return 2
	}
}

//nolint:goconst
func compatResolutionWidth(label string) int {
	switch strings.ToLower(label) {
	case "2160p", "4k":
		return 3840
	case "1080p":
		return 1920
	case "720p":
		return 1280
	case "480p":
		return 720
	default:
		if left, right, ok := strings.Cut(strings.TrimSpace(label), "x"); ok {
			if w, err := strconv.Atoi(strings.TrimSpace(left)); err == nil && w > 0 && w <= 32768 {
				if h, err := strconv.Atoi(strings.TrimSpace(right)); err == nil && h > 0 && h <= 32768 {
					return w
				}
			}
		}
		return 0
	}
}

//nolint:goconst
func compatResolutionHeight(label string) int {
	switch strings.ToLower(label) {
	case "2160p", "4k":
		return 2160
	case "1080p":
		return 1080
	case "720p":
		return 720
	case "480p":
		return 480
	default:
		if left, right, ok := strings.Cut(strings.TrimSpace(label), "x"); ok {
			if w, err := strconv.Atoi(strings.TrimSpace(left)); err == nil && w > 0 && w <= 32768 {
				if h, err := strconv.Atoi(strings.TrimSpace(right)); err == nil && h > 0 && h <= 32768 {
					return h
				}
			}
		}
		return 0
	}
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

func (h *PlaybackHandler) resolveVirtualTransport(ctx context.Context, session *Session, source PlaybackMediaSource, forceRefresh bool) (ResolvedVirtualMedia, error) {
	if session == nil {
		return ResolvedVirtualMedia{}, errors.New("compat playback session is unavailable")
	}
	return h.resolveVirtualTransportForIdentity(ctx, session.StreamAppUserID, session.ProfileID, source, forceRefresh)
}

func (h *PlaybackHandler) resolveVirtualTransportForIdentity(ctx context.Context, userID int, profileID string, source PlaybackMediaSource, forceRefresh bool) (ResolvedVirtualMedia, error) {
	uri := strings.TrimSpace(source.VirtualSourceURI)
	if !isCompatVirtualPath(uri) {
		return ResolvedVirtualMedia{}, errors.New("virtual playback source is not bound")
	}
	resolved, err := h.resolveVirtualTransportOnce(ctx, userID, profileID, source, uri, forceRefresh, nil, "")
	if err == nil {
		return resolved, nil
	}
	// A pinned ?result= candidate can go stale when the provider re-lists: the
	// catalog row survives but the provider no longer returns that result, so
	// direct play would surface a 502. Re-resolve provider-neutrally, excluding
	// the dead candidate, so the same quality selection recovers to a live
	// result instead of failing. Bounded to a single retry: a fully-down
	// provider still fails with the original error.
	failedID := compatVirtualResultCandidateID(uri)
	if failedID == "" {
		return resolved, err
	}
	neutralURI := compatVirtualNeutralURI(uri)
	if neutralURI == uri {
		return resolved, err
	}
	recovered, recoverErr := h.resolveVirtualTransportOnce(ctx, userID, profileID, source, neutralURI, true, []string{failedID}, "")
	if recoverErr != nil {
		return resolved, err
	}
	h.repairCompatVirtualPin(ctx, source, uri, recovered)
	return recovered, nil
}

func (h *PlaybackHandler) resolveVirtualTransportOnce(ctx context.Context, userID int, profileID string, source PlaybackMediaSource, uri string, forceRefresh bool, excludedCandidateIDs []string, preferredCandidateID string) (ResolvedVirtualMedia, error) {
	if h.VirtualMediaDetailedResolver != nil {
		res, err := h.VirtualMediaDetailedResolver.ResolveVirtualMediaDetailed(ctx, uri, source.VirtualSourceOwnerInstallationID, userID, profileID, forceRefresh, excludedCandidateIDs, preferredCandidateID)
		res.RequestHeaders = cloneHeaderMap(res.RequestHeaders)
		return res, err
	}
	if forceRefresh && h.VirtualMediaRefreshResolver != nil {
		url, err := h.VirtualMediaRefreshResolver.RefreshVirtualMedia(ctx, uri, source.VirtualSourceOwnerInstallationID, userID, profileID)
		return ResolvedVirtualMedia{URL: url, URI: uri}, err
	}
	if h.VirtualMediaResolver != nil {
		url, err := h.VirtualMediaResolver.ResolveVirtualMedia(ctx, uri, source.VirtualSourceOwnerInstallationID, userID, profileID)
		return ResolvedVirtualMedia{URL: url, URI: uri}, err
	}
	return ResolvedVirtualMedia{}, errors.New("virtual playback resolver is not configured")
}

// compatVirtualPinReplacer is the subset of the file repository the recovery
// path needs to CAS-repair a dead pin after a successful re-resolve. The
// concrete *scanner.FileRepository implements it; the interface keeps the
// dependency optional so tests and non-DB resolvers need not provide it.
type compatVirtualPinReplacer interface {
	ReplaceVirtualResultPin(context.Context, int, string, string) (bool, error)
}

// repairCompatVirtualPin re-pins the catalog row to the recovered candidate
// after a stale-pin recovery, so the next playback does not re-resolve the dead
// result. Best-effort: a collision (the winning candidate already exists as a
// sibling row) is handled by ReplaceVirtualResultPin itself, and any error is
// logged without failing the in-flight stream.
func (h *PlaybackHandler) repairCompatVirtualPin(ctx context.Context, source PlaybackMediaSource, oldURI string, recovered ResolvedVirtualMedia) {
	if source.FileID <= 0 || recovered.URI == "" || recovered.URI == oldURI {
		return
	}
	replacer, ok := h.fileResolver.(compatVirtualPinReplacer)
	if !ok {
		return
	}
	repairCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, err := replacer.ReplaceVirtualResultPin(repairCtx, source.FileID, oldURI, recovered.URI); err != nil {
		slog.WarnContext(ctx, "failed to repair virtual result pin after compat recovery", "component", "jellycompat", "file_id", source.FileID, "error", err)
	}
}

// compatVirtualNeutralURI strips the concrete ?result= pick from a virtual URI,
// preserving scheme/host/path and profile so a stale candidate can be
// re-resolved provider-neutrally within the same quality selection.
func compatVirtualNeutralURI(virtualPath string) string {
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

// compatVirtualResultCandidateID returns the concrete "result=" candidate ID
// bound to a virtual URI, or "" when the URI carries no explicit pick.
func compatVirtualResultCandidateID(virtualPath string) string {
	parsed, err := url.Parse(virtualPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("result"))
}

func (h *PlaybackHandler) registerVirtualInput(ctx context.Context, session *Session, source PlaybackMediaSource, forceRefresh bool) (string, func(), error) {
	if session == nil {
		return "", nil, errors.New("compat playback session is unavailable")
	}
	return h.registerVirtualInputForIdentity(ctx, session.StreamAppUserID, session.ProfileID, source, forceRefresh)
}

// compatLocalTranscodeInput resolves the concrete input a local compat transcode
// process may open. A local file passes through untouched. A virtual row carries
// a virtual:// identity as its durable path, and no FFmpeg build can open that
// scheme: handing it over starts an encoder that exits before its first segment,
// which leaves the master manifest answering 200 while every child segment 404s
// and nothing is ever produced. The remote executor and session reconstruction
// resolve virtual sources through the relay for the same reason, so the local
// manifest path has to do it too.
//
// canonical is the durable identity a recipe card keeps, so a reconstructed
// session resolves the source again instead of replaying a dead loopback URL.
// release frees the relay registration and is safe to call more than once;
// refresh mints a fresh provider URL for a seek restart.
func (h *PlaybackHandler) compatLocalTranscodeInput(
	ctx context.Context,
	upstreamSessionID string,
	source PlaybackMediaSource,
	file *models.MediaFile,
) (inputPath, canonical string, release func(), refresh func(context.Context) (string, func(), error), err error) {
	if !isCompatVirtualSource(source) && !isCompatVirtualFile(file) {
		if file == nil {
			return "", "", nil, nil, errors.New("media file is unavailable")
		}
		return file.FilePath, "", nil, nil, nil
	}
	canonical = firstNonEmpty(strings.TrimSpace(source.VirtualSourceURI), strings.TrimSpace(source.Version.FilePath))
	if canonical == "" && file != nil {
		canonical = strings.TrimSpace(file.FilePath)
	}
	if !isCompatVirtualPath(canonical) {
		// A row flagged virtual whose frozen path is already concrete (a repaired
		// or legacy binding) needs no relay: open the file it names.
		if file != nil && file.FilePath != "" && !isCompatVirtualPath(file.FilePath) {
			return file.FilePath, "", nil, nil, nil
		}
		return "", "", nil, nil, fmt.Errorf("%w: no virtual source identity to resolve", errCompatVirtualInputUnavailable)
	}
	userID, profileID := 0, ""
	if h.sessionMgr != nil {
		if upstream, getErr := h.sessionMgr.GetSession(upstreamSessionID); getErr == nil && upstream != nil {
			userID, profileID = upstream.UserID, upstream.ProfileID
		}
	}
	bound := source
	bound.VirtualSourceURI = canonical
	resolve := func(resolveCtx context.Context, forceRefresh bool) (string, func(), error) {
		relayURL, cleanup, resolveErr := h.registerVirtualInputForIdentity(resolveCtx, userID, profileID, bound, forceRefresh)
		if resolveErr != nil {
			return "", nil, fmt.Errorf("%w: %w", errCompatVirtualInputUnavailable, resolveErr)
		}
		return relayURL, cleanup, nil
	}
	relayURL, cleanup, resolveErr := resolve(ctx, false)
	if resolveErr != nil {
		return "", "", nil, nil, resolveErr
	}
	// The session owns the registration once it starts and releases it on close;
	// every failed start releases it here. Both meet this once.
	var once sync.Once
	release = func() { once.Do(cleanup) }
	refresh = func(refreshCtx context.Context) (string, func(), error) {
		return resolve(refreshCtx, true)
	}
	return relayURL, canonical, release, refresh, nil
}

// resolveCompatTranscodeInput is the compat TranscodeManager's ResolveInput
// hook. A rebuilt encoder is handed the recipe's durable identity, which for a
// virtual row is a virtual:// path FFmpeg cannot open, so it resolves through
// the relay exactly as a fresh start does.
func (h *PlaybackHandler) resolveCompatTranscodeInput(ctx context.Context, mediaFileID, ownerInstallationID int, userID int, profileID, canonicalPath string) (string, func(), error) {
	if !isCompatVirtualPath(canonicalPath) {
		return canonicalPath, nil, nil
	}
	if h == nil || h.RemoteStreamRelay == nil {
		return "", nil, fmt.Errorf("%w: remote stream relay is not configured", errCompatVirtualInputUnavailable)
	}
	source := PlaybackMediaSource{
		FileID:                           mediaFileID,
		VirtualSourceURI:                 canonicalPath,
		VirtualSourceOwnerInstallationID: ownerInstallationID,
	}
	relayURL, cleanup, err := h.registerVirtualInputForIdentity(ctx, userID, profileID, source, false)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", errCompatVirtualInputUnavailable, err)
	}
	return relayURL, cleanup, nil
}

func (h *PlaybackHandler) registerVirtualInputForIdentity(ctx context.Context, userID int, profileID string, source PlaybackMediaSource, forceRefresh bool) (string, func(), error) {
	resolved, err := h.resolveVirtualTransportForIdentity(ctx, userID, profileID, source, forceRefresh)
	if err != nil {
		return "", nil, err
	}
	if h.RemoteStreamRelay == nil {
		return "", nil, errors.New("remote stream relay is not configured")
	}
	insecure := h.AllowPrivateStreams != nil && h.AllowPrivateStreams(effectiveVirtualOwner(resolved.OwnerID, source.VirtualSourceOwnerInstallationID))
	relayURL, cleanup, err := registerRemoteStreamInputWithHeaders(ctx, h.RemoteStreamRelay, resolved.URL, resolved.RequestHeaders, insecure)
	if err != nil {
		return "", nil, err
	}
	return relayURL, cleanup, nil
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
	err = h.proxyVirtualStream(streamWriter, r, source, resolved)
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
	err = h.proxyVirtualStream(streamWriter, r, source, resolved)
	if errors.As(err, &proxyErr) && proxyErr.Started {
		return nil
	}
	return err
}

// proxyVirtualStream routes the resolved URL through the strict SSRF-protected
// proxy, or through the insecure proxy when the core
// virtual_library.allow_private_streams opt-in permits private/local stream
// destinations.
func (h *PlaybackHandler) proxyVirtualStream(w http.ResponseWriter, r *http.Request, source PlaybackMediaSource, resolved ResolvedVirtualMedia) error {
	insecure := h.AllowPrivateStreams != nil && h.AllowPrivateStreams(effectiveVirtualOwner(resolved.OwnerID, source.VirtualSourceOwnerInstallationID))
	if hp, ok := h.RemoteStreamRelay.(headerRemoteStreamProxy); ok {
		if insecure {
			return hp.ProxyInsecureWithHeaders(w, r, resolved.URL, resolved.RequestHeaders)
		}
		return hp.ProxyWithHeaders(w, r, resolved.URL, resolved.RequestHeaders)
	}
	if insecure {
		return h.RemoteStreamRelay.ProxyInsecure(w, r, resolved.URL)
	}
	return h.RemoteStreamRelay.Proxy(w, r, resolved.URL)
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
