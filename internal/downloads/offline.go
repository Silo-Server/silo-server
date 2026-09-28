package downloads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// authorizeManagedAsset applies invariant-2 row authorization (user, profile,
// device) and the revoked guard to a managed entry before any asset is served.
// The per-profile content-access re-check is performed by each caller (via
// GetItemDetail or EnsureAccessible) before bytes leave the server.
func (s *Service) authorizeManagedAsset(ctx context.Context, userID int, profileID, deviceID, downloadID string) (*Download, error) {
	if _, err := s.enabledConfig(ctx); err != nil {
		return nil, err
	}
	if profileID == "" || deviceID == "" {
		return nil, ErrProfileRequired
	}
	dl, err := s.repo.GetManagedByID(ctx, downloadID, userID, profileID, deviceID)
	if err != nil {
		return nil, err
	}
	if dl.Status == StatusRevoked {
		return nil, fmt.Errorf("download is revoked: %w", ErrDownloadNotActive)
	}
	return dl, nil
}

// BuildManifest returns the offline manifest for a managed entry, authorized on
// (user, profile, device) with a per-profile content-access re-check inside the
// builder's GetItemDetail call.
func (s *Service) BuildManifest(ctx context.Context, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) (*OfflineManifest, error) {
	dl, err := s.authorizeManagedAsset(ctx, userID, profileID, deviceID, downloadID)
	if err != nil {
		return nil, err
	}
	if s.manifest == nil {
		return nil, ErrManifestUnavailable
	}
	return s.manifest.Build(ctx, dl, filter)
}

// SkippedManifest reports a batch entry whose manifest could not be built —
// one bad episode (revoked, deleted from the catalog, access-filtered) must
// not make the rest of a season's manifests unfetchable.
type SkippedManifest struct {
	DownloadID string `json:"download_id"`
	Reason     string `json:"reason"` // revoked | not_found | error
}

// BuildBatchManifests returns the manifests for every managed entry in a batch
// owned by the calling profile/device, plus the entries it had to skip.
// Entries in one batch share a series, so the series detail is resolved once.
func (s *Service) BuildBatchManifests(ctx context.Context, userID int, profileID, deviceID, batchID string, filter catalog.AccessFilter) ([]*OfflineManifest, []SkippedManifest, error) {
	if _, err := s.enabledConfig(ctx); err != nil {
		return nil, nil, err
	}
	if profileID == "" || deviceID == "" {
		return nil, nil, ErrProfileRequired
	}
	if s.manifest == nil {
		return nil, nil, ErrManifestUnavailable
	}
	rows, err := s.repo.ListManagedByBatch(ctx, userID, profileID, deviceID, batchID)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, ErrNotFound
	}
	return s.buildBatchManifestRows(ctx, rows, filter)
}

func (s *Service) buildBatchManifestRows(ctx context.Context, rows []*Download, filter catalog.AccessFilter) ([]*OfflineManifest, []SkippedManifest, error) {
	out := make([]*OfflineManifest, 0, len(rows))
	skipped := make([]SkippedManifest, 0)
	seriesCache := make(map[string]*catalog.ItemDetail, 1)
	for _, dl := range rows {
		if dl.Status == StatusRevoked {
			skipped = append(skipped, SkippedManifest{DownloadID: dl.ID, Reason: "revoked"})
			continue
		}
		m, err := s.manifest.build(ctx, dl, filter, seriesCache, false)
		if err != nil {
			reason := "error"
			if errors.Is(err, catalog.ErrItemNotFound) {
				reason = "not_found"
			} else {
				slog.WarnContext(ctx, "batch manifest build failed", "component", "downloads", "download_id", dl.ID, "batch_id", dl.BatchID, "error", err)
			}
			skipped = append(skipped, SkippedManifest{DownloadID: dl.ID, Reason: reason})
			continue
		}
		out = append(out, m)
	}
	return out, skipped, nil
}

// ServeArtwork streams poster/backdrop/logo bytes for a managed entry through
// the image resolver (never a presigned redirect), re-checking per-profile
// access via GetItemDetail before serving.
func (s *Service) ServeArtwork(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID, kind string, filter catalog.AccessFilter) error {
	dl, err := s.authorizeManagedAsset(ctx, userID, profileID, deviceID, downloadID)
	if err != nil {
		return err
	}
	if s.artworkSource == nil {
		return ErrManifestUnavailable
	}
	detail, err := s.artworkSource.GetItemDetail(ctx, manifestContentID(dl), filter)
	if err != nil {
		return err
	}
	var imageURL string
	switch kind {
	case "poster":
		imageURL = detail.PosterURL
	case "backdrop":
		imageURL = detail.BackdropURL
	case "logo":
		imageURL = detail.LogoURL
	default:
		return ErrAssetNotFound
	}
	if imageURL == "" {
		return ErrAssetNotFound
	}
	err = s.streamArtwork(ctx, w, r, imageURL)
	if errors.Is(err, ErrAssetUnavailable) {
		logArtworkUnavailable(ctx, downloadID, kind, err)
	}
	return err
}

// logArtworkUnavailable records a failing artwork store. The error itself can
// quote the presigned URL (a malformed redirect's Location, for one), so only
// the upstream status or whether the fetch timed out is logged.
func logArtworkUnavailable(ctx context.Context, downloadID, kind string, err error) {
	attrs := []any{"component", "downloads", "download_id", downloadID, "kind", kind}
	if status, ok := errors.AsType[artworkStatusError](err); ok {
		attrs = append(attrs, "upstream_status", int(status))
	} else if netErr, ok := errors.AsType[net.Error](err); ok {
		attrs = append(attrs, "timeout", netErr.Timeout())
	}
	slog.WarnContext(ctx, "download artwork unavailable", attrs...)
}

// artworkStatusError is the artwork store's answer when it wasn't 200.
type artworkStatusError int

func (e artworkStatusError) Error() string {
	return "artwork upstream status " + strconv.Itoa(int(e))
}

// artworkClient fetches artwork when the service has no client of its own.
// Artwork is small, so a store that hasn't answered in this time is failing.
var artworkClient = &http.Client{Timeout: 30 * time.Second}

func (s *Service) artworkHTTPClient() *http.Client {
	if s.httpClient != nil {
		return s.httpClient
	}
	return artworkClient
}

func (s *Service) streamArtwork(ctx context.Context, w http.ResponseWriter, _ *http.Request, imageURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		// Not wrapped: the parse error quotes the presigned URL.
		return errors.New("building artwork request: invalid artwork URL")
	}
	if (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" {
		// Local artwork storage signs server-relative URLs, which can't be
		// fetched over HTTP. Retrying won't help.
		return errors.New("fetching artwork: artwork URL is not an absolute http(s) URL")
	}
	resp, err := s.artworkHTTPClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// The client went away; the store isn't at fault.
			return ctx.Err()
		}
		// A failed request's error text repeats the presigned URL; keep only
		// the cause.
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return fmt.Errorf("fetching artwork: %w: %w", ErrAssetUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
		// Also ErrAssetNotFound, which the frozen v1 route answers with 404
		// as it always has.
		return fmt.Errorf("%w: %w: %w", artworkStatusError(resp.StatusCode), ErrAssetUnavailable, ErrAssetNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %w", artworkStatusError(resp.StatusCode), ErrAssetNotFound)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	// Artwork is immutable for a stored manifest; let the client cache it once.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	store := &storeReader{Reader: resp.Body}
	if _, err := io.Copy(w, store); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if store.err != nil {
			// The store stopped sending, or timed out mid-body. Before the
			// first byte is written, v2 can still answer 503.
			return fmt.Errorf("streaming artwork: %w: %w", ErrAssetUnavailable, err)
		}
		return fmt.Errorf("streaming artwork: %w", err)
	}
	return nil
}

// storeReader remembers why reading from the artwork store failed, so a
// failed write to the client isn't blamed on the store.
type storeReader struct {
	io.Reader
	err error
}

func (r *storeReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

// ServeSubtitle streams a subtitle asset (external sidecar or downloaded S3 file)
// for a managed entry, authorized on (user, profile, device) with a per-profile
// content-access re-check. ref encodes "external:{index}" or "downloaded:{id}".
func (s *Service) ServeSubtitle(ctx context.Context, w http.ResponseWriter, _ *http.Request, userID int, profileID, deviceID, downloadID, ref string, filter catalog.AccessFilter) error {
	dl, err := s.authorizeManagedAsset(ctx, userID, profileID, deviceID, downloadID)
	if err != nil {
		return err
	}
	if err := s.itemAccess.EnsureAccessible(ctx, dl.ContentID, filter); err != nil {
		return err
	}
	notifyServeAuthorized(ctx, FileTarget{DownloadID: dl.ID, MediaFileID: dl.MediaFileID})

	kind, value, err := parseSubtitleRef(ref)
	if err != nil {
		return err
	}
	switch kind {
	case "external":
		idx := value
		file, err := s.fileRepo.GetByID(ctx, dl.MediaFileID)
		if err != nil {
			return fmt.Errorf("loading media file: %w", err)
		}
		if file == nil || idx < 0 || idx >= len(file.ExternalSubtitles) {
			return ErrAssetNotFound
		}
		ext := file.ExternalSubtitles[idx]
		data, err := playback.LoadExternalSubtitleRaw(ext.Path)
		if err != nil {
			return fmt.Errorf("reading external subtitle: %w", ErrAssetNotFound)
		}
		writeSubtitle(w, ext.Format, data)
		return nil
	case "downloaded":
		if s.subtitleSource == nil {
			return ErrManifestUnavailable
		}
		sub, data, err := s.subtitleSource.GetSubtitleContent(ctx, value)
		if err != nil {
			return fmt.Errorf("loading downloaded subtitle: %w", ErrAssetNotFound)
		}
		// The subtitle must belong to this download's media file; a download id
		// never grants access to an arbitrary subtitle id.
		if sub == nil || sub.MediaFileID != dl.MediaFileID {
			return ErrAssetNotFound
		}
		writeSubtitle(w, string(sub.Format), data)
		return nil
	default:
		return ErrInvalidSubtitleRef
	}
}

// parseSubtitleRef parses a subtitle reference of the form "external:{index}"
// or "downloaded:{id}" into its kind and integer value.
func parseSubtitleRef(ref string) (kind string, value int, err error) {
	k, v, ok := strings.Cut(ref, ":")
	if !ok {
		return "", 0, ErrInvalidSubtitleRef
	}
	switch k {
	case "external", "downloaded":
		n, perr := strconv.Atoi(v)
		if perr != nil {
			return "", 0, ErrInvalidSubtitleRef
		}
		return k, n, nil
	default:
		return "", 0, ErrInvalidSubtitleRef
	}
}

// writeSubtitle writes subtitle bytes with a format-appropriate content type
// (the shared subtitles mapping — no local copy to drift).
func writeSubtitle(w http.ResponseWriter, format string, data []byte) {
	w.Header().Set("Content-Type", subtitles.SubtitleContentType(subtitles.SubtitleFormat(strings.ToLower(format))))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(data)
}
