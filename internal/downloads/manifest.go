package downloads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// manifestVersion is bumped whenever the OfflineManifest DTO shape changes.
const manifestVersion = 2

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

// OfflineAudioTrack describes audio streams the client may expose offline.
type OfflineAudioTrack struct {
	Index      int    `json:"index"`
	Title      string `json:"title,omitempty"`
	Language   string `json:"language,omitempty"`
	Codec      string `json:"codec,omitempty"`
	Layout     string `json:"layout,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Bitrate    int    `json:"bitrate,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
	Default    bool   `json:"default"`
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

// OfflineIntegrity gives clients stable metadata for local file validation.
type OfflineIntegrity struct {
	ExpectedBytes int64  `json:"expected_bytes"`
	MediaFileHash string `json:"media_file_hash,omitempty"`
	MetadataETag  string `json:"metadata_etag"`
}

// OfflineManifest is the stable, presigned-URL-free bundle a client stores to
// play a managed download fully offline.
type OfflineManifest struct {
	DownloadID        string `json:"download_id"`
	ContentID         string `json:"content_id"`
	EpisodeID         string `json:"episode_id,omitempty"`
	Type              string `json:"type"`
	Revision          int    `json:"revision"`
	Quality           string `json:"quality"`
	EffectiveQuality  string `json:"effective_quality"`
	DeliveryFormat    string `json:"delivery_format"`
	TargetBitrateKbps int    `json:"target_bitrate_kbps"`
	MediaFileID       int    `json:"media_file_id"`
	FileSize          int64  `json:"file_size"`

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

	Container               string              `json:"container"`
	CodecVideo              string              `json:"codec_video"`
	CodecAudio              string              `json:"codec_audio"`
	Resolution              string              `json:"resolution"`
	HDR                     bool                `json:"hdr"`
	Duration                int                 `json:"duration_seconds"`
	SelectedAudioTrackIndex *int                `json:"selected_audio_track_index,omitempty"`
	AudioTracks             []OfflineAudioTrack `json:"audio_tracks,omitempty"`

	Chapters []OfflineChapter `json:"chapters,omitempty"`
	Intro    *Marker          `json:"intro,omitempty"`
	Credits  *Marker          `json:"credits,omitempty"`
	Recap    *Marker          `json:"recap,omitempty"`
	Preview  *Marker          `json:"preview,omitempty"`

	Subtitles []OfflineSubtitle `json:"subtitles"`

	StableIdentity OfflineIdentity  `json:"stable_identity"`
	Integrity      OfflineIntegrity `json:"integrity"`

	ManifestVersion int    `json:"manifest_version"`
	GeneratedAt     string `json:"generated_at"`
}

// ManifestBuilder assembles an OfflineManifest from the catalog detail path and
// the download's subtitle assets, stripping every presigned URL.
type ManifestBuilder struct {
	detail   ManifestSource
	subs     SubtitleSource
	fileRepo FileResolver
	// artifact resolves a download's linked prepared artifact so artifact-backed
	// manifests can describe the delivered file instead of the catalog source.
	artifact func(ctx context.Context, id string) (*Artifact, error)
}

// NewManifestBuilder constructs a ManifestBuilder. artifact may be nil when no
// prepare-to-file pipeline exists (only original downloads are servable then).
func NewManifestBuilder(detail ManifestSource, subs SubtitleSource, fileRepo FileResolver, artifact func(ctx context.Context, id string) (*Artifact, error)) *ManifestBuilder {
	return &ManifestBuilder{detail: detail, subs: subs, fileRepo: fileRepo, artifact: artifact}
}

// Build assembles the manifest for a managed entry. The filter enforces the
// requesting profile's content access (GetItemDetail returns
// catalog.ErrItemNotFound when denied).
func (b *ManifestBuilder) Build(ctx context.Context, dl *Download, filter catalog.AccessFilter) (*OfflineManifest, error) {
	return b.build(ctx, dl, filter, nil)
}

// build is Build with an optional per-batch series-detail cache: a season
// batch shares one series, so the batch endpoint resolves its detail once
// instead of once per episode.
func (b *ManifestBuilder) build(ctx context.Context, dl *Download, filter catalog.AccessFilter, seriesCache map[string]*catalog.ItemDetail) (*OfflineManifest, error) {
	detail, err := b.detail.GetItemDetail(ctx, manifestContentID(dl), filter)
	if err != nil {
		return nil, err
	}
	file := b.lookupFile(ctx, dl.MediaFileID)
	var seriesDetail *catalog.ItemDetail
	if dl.EpisodeID != "" && detail.SeriesID != "" {
		if cached, ok := seriesCache[detail.SeriesID]; ok {
			seriesDetail = cached
		} else if sd, err := b.detail.GetItemDetail(ctx, detail.SeriesID, filter); err == nil {
			seriesDetail = sd
			if seriesCache != nil {
				seriesCache[detail.SeriesID] = sd
			}
		}
	}

	m := &OfflineManifest{
		DownloadID:        dl.ID,
		ContentID:         dl.ContentID,
		EpisodeID:         dl.EpisodeID,
		Type:              detail.Type,
		Revision:          dl.Revision,
		Quality:           dl.Quality,
		EffectiveQuality:  dl.EffectiveQuality,
		DeliveryFormat:    dl.Format,
		TargetBitrateKbps: dl.TargetBitrateKbps,
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
		StableIdentity:    stableIdentity(dl, detail, seriesDetail),
		Integrity:         buildIntegrity(dl, file),
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
		m.SelectedAudioTrackIndex = v.EffectiveAudioTrackIndex
		m.AudioTracks = toOfflineAudioTracks(v.AudioTracks)
		m.Chapters = toOfflineChapters(v.Chapters)
	}

	// Remux/transcode entries deliver the prepared artifact, not the catalog
	// source: describe that file so the client picks the right decoder/tracks.
	if dl.Format != FormatOriginal && dl.ArtifactID != "" && b.artifact != nil {
		if a, err := b.artifact(ctx, dl.ArtifactID); err == nil && a != nil {
			applyArtifactParams(m, a)
		}
	}

	m.Subtitles = b.buildSubtitles(ctx, dl, file)
	return m, nil
}

// applyArtifactParams overwrites the source file's media parameters with the
// prepared artifact's target parameters. "copy" targets keep the source value.
func applyArtifactParams(m *OfflineManifest, a *Artifact) {
	if a.Container != "" {
		m.Container = a.Container
	}
	if a.CodecVideo != "" && a.CodecVideo != "copy" {
		m.CodecVideo = a.CodecVideo
	}
	if a.CodecAudio != "" && a.CodecAudio != "copy" {
		m.CodecAudio = a.CodecAudio
	}
	if a.Resolution != "" {
		m.Resolution = a.Resolution
	}
	// A prepared file contains exactly one audio stream — the track the encode
	// selected (playback.PrepareFile maps a single audio track).
	if len(m.AudioTracks) > 0 {
		idx := a.AudioTrackIndex
		if idx < 0 || idx >= len(m.AudioTracks) {
			idx = 0
		}
		track := m.AudioTracks[idx]
		track.Index = 0
		track.Default = true
		if a.CodecAudio != "" && a.CodecAudio != "copy" {
			track.Codec = a.CodecAudio
		}
		m.AudioTracks = []OfflineAudioTrack{track}
		selected := 0
		m.SelectedAudioTrackIndex = &selected
	}
}

// buildSubtitles enumerates external (sidecar) + downloaded (S3) subtitle assets
// for the download's media file (already loaded by build — no re-fetch).
// Embedded tracks live inside the downloaded video file and need no separate
// fetch.
func (b *ManifestBuilder) buildSubtitles(ctx context.Context, dl *Download, file *models.MediaFile) []OfflineSubtitle {
	out := []OfflineSubtitle{}

	if file != nil {
		for i, ext := range file.ExternalSubtitles {
			var size int64
			if info, statErr := os.Stat(ext.Path); statErr == nil {
				size = info.Size()
			}
			out = append(out, OfflineSubtitle{
				Language:        ext.Language,
				Format:          ext.Format,
				Forced:          ext.Forced,
				HearingImpaired: ext.HearingImpaired,
				External:        true,
				FetchURL:        subtitleProxyURL(dl.ID, fmt.Sprintf("external:%d", i)),
				FileSize:        size,
			})
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

func (b *ManifestBuilder) lookupFile(ctx context.Context, mediaFileID int) *models.MediaFile {
	if b.fileRepo == nil || mediaFileID <= 0 {
		return nil
	}
	file, err := b.fileRepo.GetByID(ctx, mediaFileID)
	if err != nil {
		return nil
	}
	return file
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

func toOfflineAudioTracks(tracks []models.AudioTrack) []OfflineAudioTrack {
	if len(tracks) == 0 {
		return nil
	}
	out := make([]OfflineAudioTrack, 0, len(tracks))
	for i, t := range tracks {
		out = append(out, OfflineAudioTrack{
			Index:      i,
			Title:      firstNonEmpty(t.Title, t.EmbeddedTitle),
			Language:   t.Language,
			Codec:      t.Codec,
			Layout:     t.Layout,
			Channels:   t.Channels,
			Bitrate:    t.Bitrate,
			SampleRate: t.SampleRate,
			Default:    t.Default,
		})
	}
	return out
}

func stableIdentity(dl *Download, detail, seriesDetail *catalog.ItemDetail) OfflineIdentity {
	providerIDs := map[string]string{}
	addProviderIDs(providerIDs, detail)
	id := OfflineIdentity{
		StableType:  detail.Type,
		ProviderIDs: providerIDs,
		Season:      detail.SeasonNumber,
		Episode:     detail.EpisodeNumber,
	}
	if dl.EpisodeID != "" {
		id.StableType = "episode"
		seriesProviderIDs := map[string]string{}
		addProviderIDs(seriesProviderIDs, seriesDetail)
		if len(seriesProviderIDs) > 0 {
			id.SeriesProviderIDs = seriesProviderIDs
		}
	}
	if len(providerIDs) == 0 {
		id.ProviderIDs = nil
	}
	return id
}

func addProviderIDs(out map[string]string, detail *catalog.ItemDetail) {
	if detail == nil {
		return
	}
	if detail.ImdbID != "" {
		out["imdb"] = detail.ImdbID
	}
	if detail.TmdbID != "" {
		out["tmdb"] = detail.TmdbID
	}
	if detail.TvdbID != "" {
		out["tvdb"] = detail.TvdbID
	}
}

func buildIntegrity(dl *Download, file *models.MediaFile) OfflineIntegrity {
	hash := ""
	modified := ""
	if file != nil {
		hash = file.FileHash
		if file.FileModifiedAt != nil {
			modified = file.FileModifiedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%d|%d|%s",
		dl.ID, dl.Revision, dl.Format, dl.Quality,
		dl.EffectiveQuality, dl.TargetBitrateKbps, dl.FileSize, modified)))
	return OfflineIntegrity{
		ExpectedBytes: dl.FileSize,
		MediaFileHash: hash,
		MetadataETag:  hex.EncodeToString(sum[:]),
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
