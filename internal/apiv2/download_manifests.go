package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

const maxDownloadManifestBytes = 1 << 20

type DownloadManifestService interface {
	BuildManifest(context.Context, int, string, string, string, catalogpkg.AccessFilter) (*downloads.OfflineManifest, error)
	PageBatchManifests(context.Context, int, string, string, string, *downloads.RegistryPosition, int, catalogpkg.AccessFilter) (downloads.ManifestPage, error)
}

type DownloadMarker downloads.Marker

type DownloadManifest struct {
	DownloadID        string `json:"download_id"`
	ContentID         string `json:"content_id"`
	EpisodeID         string `json:"episode_id,omitempty"`
	Type              string `json:"type"`
	Revision          int    `json:"revision"`
	Quality           string `json:"quality"`
	EffectiveQuality  string `json:"effective_quality"`
	DeliveryFormat    string `json:"delivery_format"`
	TargetBitrateKbps int    `json:"target_bitrate_kbps"`
	MediaFileID       ID     `json:"media_file_id"`
	FileSize          int64  `json:"file_size"`

	Title         string   `json:"title"`
	Year          int      `json:"year,omitzero"`
	Overview      string   `json:"overview,omitempty"`
	Runtime       int      `json:"runtime,omitzero"`
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

	Container               string                        `json:"container"`
	CodecVideo              string                        `json:"codec_video"`
	CodecAudio              string                        `json:"codec_audio"`
	Resolution              string                        `json:"resolution"`
	HDR                     bool                          `json:"hdr"`
	Duration                int                           `json:"duration_seconds"`
	SelectedAudioTrackIndex *int                          `json:"selected_audio_track_index,omitempty"`
	AudioTracks             []downloads.OfflineAudioTrack `json:"audio_tracks,omitempty"`

	Chapters       []downloads.OfflineChapter `json:"chapters,omitempty"`
	Intro          *DownloadMarker            `json:"intro,omitempty"`
	Credits        *DownloadMarker            `json:"credits,omitempty"`
	Recap          *DownloadMarker            `json:"recap,omitempty"`
	Preview        *DownloadMarker            `json:"preview,omitempty"`
	MarkerSegments []MarkerOccurrence         `json:"marker_segments" doc:"All effective marker occurrences in source-time order; empty, never null"`

	Subtitles []downloads.OfflineSubtitle `json:"subtitles"`

	StableIdentity downloads.OfflineIdentity  `json:"stable_identity"`
	Integrity      downloads.OfflineIntegrity `json:"integrity"`

	ManifestVersion int     `json:"manifest_version"`
	GeneratedAt     Instant `json:"generated_at"`
}

type DownloadManifestOutput struct{ Body DownloadManifest }
type DownloadManifestInput struct {
	ID       string `path:"id" minLength:"1"`
	DeviceID string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
}
type DownloadManifestPageInput struct {
	BatchID  string `path:"batch_id" minLength:"1"`
	DeviceID string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
	Limit    int    `query:"limit" default:"3" minimum:"1" maximum:"10"`
	Cursor   string `query:"cursor"`
}
type DownloadManifestPage struct {
	Items   []DownloadManifest          `json:"items"`
	Skipped []downloads.SkippedManifest `json:"skipped"`
	Page    PageInfo                    `json:"page"`
}
type DownloadManifestPageOutput struct{ Body DownloadManifestPage }

func registerDownloadManifests(reg *Registry) {
	read := Operation{Operation: humaOp(http.MethodGet, Prefix+"/downloads/{id}/manifest", "getDownloadManifest", "downloads", "Fetch a complete offline manifest bounded to one MiB."), Class: ClassProfileScoped, ServiceBacked: true}
	read.Errors = []int{409, 413}
	Register(reg, read, reg.getDownloadManifest)
	cursors := NewCursors(reg.deps.CursorSecret)
	page := Operation{Operation: humaOp(http.MethodGet, Prefix+"/downloads/batches/{batch_id}/manifests", "listDownloadBatchManifests", "downloads", "Page complete manifests with explicit skipped results; skipped rows still advance the cursor."), Class: ClassProfileScoped, ServiceBacked: true}
	Register(reg, page, func(ctx context.Context, in *DownloadManifestPageInput) (*DownloadManifestPageOutput, error) {
		return reg.listDownloadBatchManifests(ctx, cursors, in)
	})
}
func downloadManifestOf(row *downloads.OfflineManifest) (DownloadManifest, error) {
	generated, err := time.Parse(time.RFC3339, row.GeneratedAt)
	if err != nil {
		return DownloadManifest{}, err
	}
	out := DownloadManifest{
		DownloadID:              row.DownloadID,
		ContentID:               row.ContentID,
		EpisodeID:               row.EpisodeID,
		Type:                    row.Type,
		Revision:                row.Revision,
		Quality:                 row.Quality,
		EffectiveQuality:        row.EffectiveQuality,
		DeliveryFormat:          row.DeliveryFormat,
		TargetBitrateKbps:       row.TargetBitrateKbps,
		MediaFileID:             ID(strconv.Itoa(row.MediaFileID)),
		FileSize:                row.FileSize,
		Title:                   row.Title,
		Year:                    row.Year,
		Overview:                row.Overview,
		Runtime:                 row.Runtime,
		ContentRating:           row.ContentRating,
		Genres:                  row.Genres,
		SeriesID:                row.SeriesID,
		SeriesTitle:             row.SeriesTitle,
		SeasonNumber:            row.SeasonNumber,
		EpisodeNumber:           row.EpisodeNumber,
		PosterThumbhash:         row.PosterThumbhash,
		BackdropThumbhash:       row.BackdropThumbhash,
		Container:               row.Container,
		CodecVideo:              row.CodecVideo,
		CodecAudio:              row.CodecAudio,
		Resolution:              row.Resolution,
		HDR:                     row.HDR,
		Duration:                row.Duration,
		SelectedAudioTrackIndex: row.SelectedAudioTrackIndex,
		AudioTracks:             row.AudioTracks,
		Chapters:                row.Chapters,
		Intro:                   (*DownloadMarker)(row.Intro),
		Credits:                 (*DownloadMarker)(row.Credits),
		Recap:                   (*DownloadMarker)(row.Recap),
		Preview:                 (*DownloadMarker)(row.Preview),
		MarkerSegments:          markerOccurrences(row.MarkerSegments),
		Subtitles:               row.Subtitles,
		StableIdentity:          row.StableIdentity,
		Integrity:               row.Integrity,
		ManifestVersion:         3,
		GeneratedAt:             NewInstant(generated),
		ArtworkURLs:             row.ArtworkURLs,
	}
	// The builder mints authenticated internal asset references in this
	// namespace already; only the download id is escaped for the wire. Refuse an
	// unexpected URL instead of propagating a presigned or remote one.
	rewrite := func(value string) (string, error) {
		if value == "" {
			return "", nil
		}
		suffix, ok := strings.CutPrefix(value, Prefix+"/downloads/"+row.DownloadID+"/")
		if !ok {
			return "", fmt.Errorf("unexpected offline asset reference")
		}
		return Prefix + "/downloads/" + url.PathEscape(row.DownloadID) + "/" + suffix, nil
	}
	for _, field := range []*string{&out.ArtworkURLs.Poster, &out.ArtworkURLs.Backdrop, &out.ArtworkURLs.Logo} {
		*field, err = rewrite(*field)
		if err != nil {
			return DownloadManifest{}, err
		}
	}
	out.Subtitles = append([]downloads.OfflineSubtitle{}, row.Subtitles...)
	for i := range out.Subtitles {
		out.Subtitles[i].FetchURL, err = rewrite(out.Subtitles[i].FetchURL)
		if err != nil {
			return DownloadManifest{}, err
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return DownloadManifest{}, err
	}
	if len(encoded) > maxDownloadManifestBytes {
		return DownloadManifest{}, NewProblem(TypePayloadTooLarge, "The offline manifest exceeds the one MiB limit.")
	}
	return out, nil
}
func (reg *Registry) getDownloadManifest(ctx context.Context, in *DownloadManifestInput) (*DownloadManifestOutput, error) {
	if reg.deps.DownloadManifests == nil {
		return nil, unavailable("offline manifests")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	row, err := reg.deps.DownloadManifests.BuildManifest(ctx, user, profile, in.DeviceID, in.ID, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, downloadProblem(err)
	}
	out, err := downloadManifestOf(row)
	if err != nil {
		return nil, manifestProjectionProblem(err)
	}
	return &DownloadManifestOutput{Body: out}, nil
}
func manifestProjectionProblem(err error) *Problem {
	if p, ok := errors.AsType[*Problem](err); ok {
		return p
	}
	return serviceProblem(err)
}
func (reg *Registry) listDownloadBatchManifests(ctx context.Context, cursors *Cursors, in *DownloadManifestPageInput) (*DownloadManifestPageOutput, error) {
	if reg.deps.DownloadManifests == nil {
		return nil, unavailable("offline manifests")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	filter, _ := json.Marshal([]string{in.DeviceID, in.BatchID})
	scope := CursorScope{OperationID: "listDownloadBatchManifests", Security: strconv.Itoa(user) + "/" + profile + "/" + viewerScopeDigest(ctx), Filter: string(filter), Sort: "-created_at,-id", Tiebreaker: "id"}
	var after *downloads.RegistryPosition
	if in.Cursor != "" {
		after = &downloads.RegistryPosition{}
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	page, err := reg.deps.DownloadManifests.PageBatchManifests(ctx, user, profile, in.DeviceID, in.BatchID, after, in.Limit, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, downloadProblem(err)
	}
	out := DownloadManifestPage{Items: []DownloadManifest{}, Skipped: append([]downloads.SkippedManifest{}, page.Skipped...)}
	for _, row := range page.Items {
		item, err := downloadManifestOf(row)
		if err != nil {
			reason := "error"
			if p, ok := errors.AsType[*Problem](err); ok && p.Status == http.StatusRequestEntityTooLarge {
				reason = "too_large"
			}
			out.Skipped = append(out.Skipped, downloads.SkippedManifest{DownloadID: row.DownloadID, Reason: reason})
			continue
		}
		out.Items = append(out.Items, item)
	}
	if page.Next != nil {
		next, err := cursors.Encode(scope, *page.Next)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out.Page = PageInfo{HasMore: true, NextCursor: next}
	}
	return &DownloadManifestPageOutput{Body: out}, nil
}
