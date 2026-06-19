package downloads

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// manifestVersion is bumped whenever the OfflineManifest DTO shape changes.
const manifestVersion = 1

const apiDownloadsPrefix = "/api/v1/downloads/"

// ManifestSource assembles catalog detail for a content id. GetItemDetail
// enforces per-profile content/library access via its filter, which doubles as
// the manifest/artwork access re-check.
type ManifestSource interface {
	GetItemDetail(ctx context.Context, contentID string, filter catalog.AccessFilter) (*catalog.ItemDetail, error)
}

// SubtitleSource enumerates and fetches downloaded (S3) subtitle assets.
type SubtitleSource interface {
	ListDownloadedSubtitles(ctx context.Context, mediaFileID int) ([]subtitles.DownloadedSubtitle, error)
	GetSubtitleContent(ctx context.Context, id int) (*subtitles.DownloadedSubtitle, []byte, error)
}

// Marker is a time range (intro/credits/recap/preview) in seconds.
type Marker struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// OfflineChapter is a chapter with only stable references (no presigned URL).
type OfflineChapter struct {
	Index              int     `json:"index"`
	Title              string  `json:"title,omitempty"`
	StartSeconds       float64 `json:"start_seconds"`
	EndSeconds         float64 `json:"end_seconds"`
	ThumbnailThumbhash string  `json:"thumbnail_thumbhash,omitempty"`
}

// OfflineSubtitle is one downloadable subtitle asset. FetchURL is an
// authenticated proxy endpoint, never a presigned URL.
type OfflineSubtitle struct {
	Language        string `json:"language"`
	Format          string `json:"format"`
	Forced          bool   `json:"forced"`
	HearingImpaired bool   `json:"hearing_impaired"`
	External        bool   `json:"external"`
	FetchURL        string `json:"fetch_url"`
	FileSize        int64  `json:"file_size,omitempty"`
}

// OfflineIdentity mirrors userstore.WatchIdentity so a client can re-resolve
// content_id after a server-side rescan.
type OfflineIdentity struct {
	StableType        string            `json:"stable_type,omitempty"`
	ProviderIDs       map[string]string `json:"provider_ids,omitempty"`
	SeriesProviderIDs map[string]string `json:"series_provider_ids,omitempty"`
	Season            *int              `json:"season,omitempty"`
	Episode           *int              `json:"episode,omitempty"`
}

// OfflineManifest is the stable, presigned-URL-free bundle a client stores to
// play a managed download fully offline.
type OfflineManifest struct {
	DownloadID  string `json:"download_id"`
	ContentID   string `json:"content_id"`
	EpisodeID   string `json:"episode_id,omitempty"`
	Type        string `json:"type"`
	Format      string `json:"format"`
	MediaFileID int    `json:"media_file_id"`
	FileSize    int64  `json:"file_size"`

	Title         string   `json:"title"`
	Year          int      `json:"year,omitempty"`
	Overview      string   `json:"overview,omitempty"`
	Runtime       int      `json:"runtime,omitempty"`
	ContentRating string   `json:"content_rating,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	SeriesID      string   `json:"series_id,omitempty"`
	SeriesTitle   string   `json:"series_title,omitempty"`
	SeasonNumber  *int     `json:"season_number,omitempty"`
	EpisodeNumber *int     `json:"episode_number,omitempty"`

	// Artwork: stable thumbhashes inline + authenticated proxy URLs (never
	// presigned S3 URLs). The client downloads the proxy URLs once.
	PosterThumbhash   string `json:"poster_thumbhash,omitempty"`
	BackdropThumbhash string `json:"backdrop_thumbhash,omitempty"`
	ArtworkURLs       struct {
		Poster   string `json:"poster,omitempty"`
		Backdrop string `json:"backdrop,omitempty"`
		Logo     string `json:"logo,omitempty"`
	} `json:"artwork_urls"`

	Container  string `json:"container"`
	CodecVideo string `json:"codec_video"`
	CodecAudio string `json:"codec_audio"`
	Resolution string `json:"resolution"`
	HDR        bool   `json:"hdr"`
	Duration   int    `json:"duration_seconds"`

	Chapters []OfflineChapter `json:"chapters,omitempty"`
	Intro    *Marker          `json:"intro,omitempty"`
	Credits  *Marker          `json:"credits,omitempty"`
	Recap    *Marker          `json:"recap,omitempty"`
	Preview  *Marker          `json:"preview,omitempty"`

	Subtitles []OfflineSubtitle `json:"subtitles"`

	StableIdentity OfflineIdentity `json:"stable_identity"`

	ManifestVersion int    `json:"manifest_version"`
	GeneratedAt     string `json:"generated_at"`
}

// ManifestBuilder assembles an OfflineManifest from the catalog detail path and
// the download's subtitle assets, stripping every presigned URL.
type ManifestBuilder struct {
	detail   ManifestSource
	subs     SubtitleSource
	fileRepo FileResolver
}

// NewManifestBuilder constructs a ManifestBuilder.
func NewManifestBuilder(detail ManifestSource, subs SubtitleSource, fileRepo FileResolver) *ManifestBuilder {
	return &ManifestBuilder{detail: detail, subs: subs, fileRepo: fileRepo}
}

// Build assembles the manifest for a managed entry. The filter enforces the
// requesting profile's content access (GetItemDetail returns
// catalog.ErrItemNotFound when denied).
func (b *ManifestBuilder) Build(ctx context.Context, dl *Download, filter catalog.AccessFilter) (*OfflineManifest, error) {
	detail, err := b.detail.GetItemDetail(ctx, manifestContentID(dl), filter)
	if err != nil {
		return nil, err
	}

	m := &OfflineManifest{
		DownloadID:        dl.ID,
		ContentID:         dl.ContentID,
		EpisodeID:         dl.EpisodeID,
		Type:              detail.Type,
		Format:            dl.Format,
		MediaFileID:       dl.MediaFileID,
		FileSize:          dl.FileSize,
		Title:             detail.Title,
		Year:              detail.Year,
		Overview:          detail.Overview,
		Runtime:           detail.Runtime,
		ContentRating:     detail.ContentRating,
		Genres:            detail.Genres,
		SeriesID:          detail.SeriesID,
		SeriesTitle:       detail.SeriesTitle,
		SeasonNumber:      detail.SeasonNumber,
		EpisodeNumber:     detail.EpisodeNumber,
		PosterThumbhash:   detail.PosterThumbhash,
		BackdropThumbhash: detail.BackdropThumbhash,
		Intro:             toMarker(detail.Intro),
		Credits:           toMarker(detail.Credits),
		Recap:             toMarker(detail.Recap),
		Preview:           toMarker(detail.Preview),
		StableIdentity:    stableIdentity(dl, detail),
		ManifestVersion:   manifestVersion,
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
	}

	// Artwork: emit a proxy URL only when the source actually has the image.
	if detail.PosterURL != "" {
		m.ArtworkURLs.Poster = artworkProxyURL(dl.ID, "poster")
	}
	if detail.BackdropURL != "" {
		m.ArtworkURLs.Backdrop = artworkProxyURL(dl.ID, "backdrop")
	}
	if detail.LogoURL != "" {
		m.ArtworkURLs.Logo = artworkProxyURL(dl.ID, "logo")
	}

	if v := pickVersion(detail, dl.MediaFileID); v != nil {
		m.Container = v.Container
		m.CodecVideo = v.CodecVideo
		m.CodecAudio = v.CodecAudio
		m.Resolution = v.Resolution
		m.HDR = v.HDR
		m.Duration = v.Duration
		m.Chapters = toOfflineChapters(v.Chapters)
	}

	m.Subtitles = b.buildSubtitles(ctx, dl)
	return m, nil
}

// buildSubtitles enumerates external (sidecar) + downloaded (S3) subtitle assets
// for the download's media file. Embedded tracks live inside the downloaded
// video file and need no separate fetch.
func (b *ManifestBuilder) buildSubtitles(ctx context.Context, dl *Download) []OfflineSubtitle {
	out := []OfflineSubtitle{}

	if b.fileRepo != nil {
		if file, err := b.fileRepo.GetByID(ctx, dl.MediaFileID); err == nil && file != nil {
			for i, ext := range file.ExternalSubtitles {
				out = append(out, OfflineSubtitle{
					Language:        ext.Language,
					Format:          ext.Format,
					Forced:          ext.Forced,
					HearingImpaired: ext.HearingImpaired,
					External:        true,
					FetchURL:        subtitleProxyURL(dl.ID, fmt.Sprintf("external:%d", i)),
				})
			}
		}
	}

	if b.subs != nil {
		if downloaded, err := b.subs.ListDownloadedSubtitles(ctx, dl.MediaFileID); err == nil {
			for _, sub := range downloaded {
				out = append(out, OfflineSubtitle{
					Language:        sub.Language,
					Format:          string(sub.Format),
					HearingImpaired: sub.HearingImpaired,
					External:        false,
					FetchURL:        subtitleProxyURL(dl.ID, fmt.Sprintf("downloaded:%d", sub.ID)),
				})
			}
		}
	}

	return out
}

// manifestContentID resolves the item the manifest describes: the episode's own
// content id for episode entries, otherwise the movie's content id.
func manifestContentID(dl *Download) string {
	if dl.EpisodeID != "" {
		return dl.EpisodeID
	}
	return dl.ContentID
}

func artworkProxyURL(downloadID, kind string) string {
	return apiDownloadsPrefix + downloadID + "/artwork/" + kind
}

func subtitleProxyURL(downloadID, ref string) string {
	return apiDownloadsPrefix + downloadID + "/subtitles/" + ref
}

func pickVersion(detail *catalog.ItemDetail, mediaFileID int) *catalog.FileVersion {
	for i := range detail.Versions {
		if detail.Versions[i].FileID == mediaFileID {
			return &detail.Versions[i]
		}
	}
	if len(detail.Versions) > 0 {
		return &detail.Versions[0]
	}
	return nil
}

func toMarker(m *catalog.Marker) *Marker {
	if m == nil {
		return nil
	}
	return &Marker{Start: m.Start, End: m.End}
}

func toOfflineChapters(chapters []catalog.VersionChapter) []OfflineChapter {
	if len(chapters) == 0 {
		return nil
	}
	out := make([]OfflineChapter, 0, len(chapters))
	for _, c := range chapters {
		out = append(out, OfflineChapter{
			Index:              c.Index,
			Title:              c.Title,
			StartSeconds:       c.StartSeconds,
			EndSeconds:         c.EndSeconds,
			ThumbnailThumbhash: c.ThumbnailThumbhash,
		})
	}
	return out
}

func stableIdentity(dl *Download, detail *catalog.ItemDetail) OfflineIdentity {
	providerIDs := map[string]string{}
	if detail.ImdbID != "" {
		providerIDs["imdb"] = detail.ImdbID
	}
	if detail.TmdbID != "" {
		providerIDs["tmdb"] = detail.TmdbID
	}
	if detail.TvdbID != "" {
		providerIDs["tvdb"] = detail.TvdbID
	}
	id := OfflineIdentity{
		StableType:  detail.Type,
		ProviderIDs: providerIDs,
		Season:      detail.SeasonNumber,
		Episode:     detail.EpisodeNumber,
	}
	if dl.EpisodeID != "" {
		id.StableType = "episode"
	}
	if len(providerIDs) == 0 {
		id.ProviderIDs = nil
	}
	return id
}
