package downloads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	out := make([]*OfflineManifest, 0, len(rows))
	skipped := make([]SkippedManifest, 0)
	seriesCache := make(map[string]*catalog.ItemDetail, 1)
	for _, dl := range rows {
		if dl.Status == StatusRevoked {
			skipped = append(skipped, SkippedManifest{DownloadID: dl.ID, Reason: "revoked"})
			continue
		}
		m, err := s.manifest.build(ctx, dl, filter, seriesCache)
		if err != nil {
			reason := "error"
			if errors.Is(err, catalog.ErrItemNotFound) {
				reason = "not_found"
			} else {
				slog.WarnContext(ctx, "batch manifest build failed", "component", "downloads", "download_id", dl.ID, "batch_id", batchID, "error", err)
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
	var url string
	switch kind {
	case "poster":
		url = detail.PosterURL
	case "backdrop":
		url = detail.BackdropURL
	case "logo":
		url = detail.LogoURL
	default:
		return ErrAssetNotFound
	}
	if url == "" {
		return ErrAssetNotFound
	}
	return s.streamArtwork(ctx, w, r, url)
}

func (s *Service) streamArtwork(ctx context.Context, w http.ResponseWriter, _ *http.Request, url string) error {
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building artwork request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching artwork: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("artwork upstream status %d: %w", resp.StatusCode, ErrAssetNotFound)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	// Artwork is immutable for a stored manifest; let the client cache it once.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("streaming artwork: %w", err)
	}
	return nil
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
