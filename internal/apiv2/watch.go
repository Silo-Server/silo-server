package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The watch domain: what a client needs to start playing one item, and the
// manual watched/unwatched mark.

// WatchDetailInput is the getWatchState request.
type WatchDetailInput struct {
	ID        ID     `path:"id" doc:"A movie, episode, audiobook or ebook; a series is not directly playable" example:"movie:heat-1995"`
	FileID    ID     `query:"file_id" doc:"Prefer this file when the item has several versions" example:"42"`
	LibraryID ID     `query:"library_id" doc:"Present the item as a member of this library" example:"1"`
	ImageSize string `query:"image_size" enum:"small,medium,large,original" doc:"Artwork variant to presign; absent picks each surface's default" example:"medium"`
}

// WatchDetailOutput is the getWatchState response.
type WatchDetailOutput struct {
	Body WatchDetail
}

// WatchDetail is the playable detail of one item: its file versions, playback
// variants, subtitles, markers, and the acting profile's progress.
type WatchDetail struct {
	ContentID                       string                  `json:"content_id" example:"movie:heat-1995"`
	Type                            string                  `json:"type" doc:"movie, episode, audiobook, ebook" example:"movie"`
	Title                           string                  `json:"title" example:"Heat"`
	Year                            int                     `json:"year,omitempty" example:"1995"`
	Overview                        string                  `json:"overview,omitempty"`
	Versions                        []WatchFileVersion      `json:"versions" doc:"Every playable file of the item; empty, never null"`
	PlaybackVariants                []WatchPlaybackVariant  `json:"playback_variants,omitempty" doc:"Logical watch choices, each spanning one or more ordered parts"`
	Subtitles                       []WatchSubtitle         `json:"subtitles" doc:"Empty, never null"`
	Intro                           *WatchMarker            `json:"intro,omitempty"`
	Credits                         *WatchMarker            `json:"credits,omitempty"`
	Recap                           *WatchMarker            `json:"recap,omitempty"`
	Preview                         *WatchMarker            `json:"preview,omitempty"`
	UserData                        *WatchUserData          `json:"user_data,omitempty" doc:"The acting profile's progress; absent without a profile or progress"`
	SeriesID                        string                  `json:"series_id,omitempty" doc:"Owning series of an episode"`
	SeriesTitle                     string                  `json:"series_title,omitempty"`
	SeasonNumber                    int                     `json:"season_number,omitempty"`
	EpisodeNumber                   int                     `json:"episode_number,omitempty"`
	EffectiveSubtitleLanguage       *string                 `json:"effective_subtitle_language,omitempty" doc:"The subtitle language the profile's preferences resolve to"`
	EffectiveSubtitleMode           *string                 `json:"effective_subtitle_mode,omitempty"`
	EffectiveShowForcedSubtitles    *bool                   `json:"effective_show_forced_subtitles,omitempty"`
	EffectiveSubtitleTrackSignature *WatchSubtitleSignature `json:"effective_subtitle_track_signature,omitempty" doc:"The subtitle track the profile last chose on this item"`
	EffectiveVersionResolution      *string                 `json:"effective_version_resolution,omitempty" doc:"The version resolution the profile last played"`
	EffectiveVersionHDR             *bool                   `json:"effective_version_hdr,omitempty"`
	EffectiveVersionCodecVideo      *string                 `json:"effective_version_codec_video,omitempty"`
	EffectiveVersionEditionKey      *string                 `json:"effective_version_edition_key,omitempty"`
}

// WatchFileVersion is one playable file of the item.
type WatchFileVersion struct {
	FileID                   ID                   `json:"file_id" example:"42"`
	FileName                 string               `json:"file_name,omitempty"`
	FilePath                 string               `json:"file_path,omitempty" doc:"Only for accounts allowed to see paths"`
	Resolution               string               `json:"resolution" example:"1080p"`
	CodecVideo               string               `json:"codec_video" example:"h264"`
	CodecAudio               string               `json:"codec_audio" example:"eac3"`
	HDR                      bool                 `json:"hdr"`
	Container                string               `json:"container" example:"mkv"`
	FileSize                 int64                `json:"file_size" doc:"Bytes"`
	DurationSeconds          int                  `json:"duration_seconds" example:"10200"`
	Bitrate                  int                  `json:"bitrate" doc:"Bits per second"`
	AddedAt                  Instant              `json:"added_at" example:"2026-01-02T03:04:05.000Z"`
	EditionRaw               string               `json:"edition_raw,omitempty"`
	EditionKey               string               `json:"edition_key,omitempty"`
	PresentationKind         string               `json:"presentation_kind,omitempty"`
	PresentationGroupKey     string               `json:"presentation_group_key,omitempty"`
	PresentationPartIndex    int                  `json:"presentation_part_index,omitempty"`
	PresentationPartTotal    int                  `json:"presentation_part_total,omitempty"`
	MultiEpisodeStart        int                  `json:"multi_episode_start,omitempty"`
	MultiEpisodeEnd          int                  `json:"multi_episode_end,omitempty"`
	EffectiveAudioTrackIndex *int                 `json:"effective_audio_track_index,omitempty"`
	EffectiveAudioLanguage   string               `json:"effective_audio_language,omitempty"`
	VideoTracks              []WatchVideoTrack    `json:"video_tracks,omitempty"`
	AudioTracks              []WatchAudioTrack    `json:"audio_tracks,omitempty"`
	SubtitleTracks           []WatchSubtitleTrack `json:"subtitle_tracks,omitempty"`
	Chapters                 []WatchChapter       `json:"chapters,omitempty"`
	Intro                    *WatchMarker         `json:"intro,omitempty"`
	Credits                  *WatchMarker         `json:"credits,omitempty"`
	Recap                    *WatchMarker         `json:"recap,omitempty"`
	Preview                  *WatchMarker         `json:"preview,omitempty"`
}

// WatchVideoTrack is one video stream of a file as the scanner probed it.
type WatchVideoTrack struct {
	Title               string `json:"title,omitempty"`
	Codec               string `json:"codec,omitempty" example:"hevc"`
	DolbyVision         string `json:"dolby_vision,omitempty"`
	DVProfile           int    `json:"dv_profile,omitempty"`
	DVLevel             int    `json:"dv_level,omitempty"`
	DVBLCompatID        int    `json:"dv_bl_compat_id,omitempty"`
	DVConfigPresent     bool   `json:"dv_config_present"`
	DVBLCompatIDPresent bool   `json:"dv_bl_compat_id_present"`
	DVBLPresent         bool   `json:"dv_bl_present,omitempty"`
	DVRPUPresent        bool   `json:"dv_rpu_present,omitempty"`
	DVELPresent         bool   `json:"dv_el_present,omitempty"`
	DVEnhancementLayer  string `json:"dv_enhancement_layer,omitempty" doc:"none, mel, fel, unknown"`
	HDR10Plus           bool   `json:"hdr10_plus,omitempty"`
	Profile             string `json:"profile,omitempty"`
	Level               int    `json:"level,omitempty"`
	Width               int    `json:"width,omitempty"`
	Height              int    `json:"height,omitempty"`
	AspectRatio         string `json:"aspect_ratio,omitempty"`
	Interlaced          bool   `json:"interlaced"`
	FrameRate           string `json:"frame_rate,omitempty"`
	Bitrate             int    `json:"bitrate,omitempty"`
	VideoRange          string `json:"video_range,omitempty"`
	VideoRangeType      string `json:"video_range_type,omitempty"`
	ColorRange          string `json:"color_range,omitempty"`
	ColorPrimaries      string `json:"color_primaries,omitempty"`
	ColorSpace          string `json:"color_space,omitempty"`
	ColorTransfer       string `json:"color_transfer,omitempty"`
	BitDepth            int    `json:"bit_depth,omitempty"`
	PixelFormat         string `json:"pixel_format,omitempty"`
	ReferenceFrames     int    `json:"reference_frames,omitempty"`
}

// WatchAudioTrack is one audio stream of a file.
type WatchAudioTrack struct {
	Title         string `json:"title,omitempty"`
	EmbeddedTitle string `json:"embedded_title,omitempty"`
	Language      string `json:"language,omitempty" example:"eng"`
	Codec         string `json:"codec,omitempty" example:"eac3"`
	Profile       string `json:"profile,omitempty"`
	Layout        string `json:"layout,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	Bitrate       int    `json:"bitrate,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	BitDepth      int    `json:"bit_depth,omitempty"`
	Default       bool   `json:"default"`
}

// WatchSubtitleTrack is one subtitle track a version carries, embedded or
// external.
type WatchSubtitleTrack struct {
	Index           int    `json:"index,omitempty"`
	Language        string `json:"language,omitempty" example:"eng"`
	Codec           string `json:"codec,omitempty"`
	Title           string `json:"title,omitempty"`
	EmbeddedTitle   string `json:"embedded_title,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	Forced          bool   `json:"forced"`
	Default         bool   `json:"default"`
	HearingImpaired bool   `json:"hearing_impaired"`
	External        bool   `json:"external"`
	FileName        string `json:"file_name,omitempty"`
}

// WatchChapter is one chapter of a version.
type WatchChapter struct {
	Index              int     `json:"index"`
	Title              string  `json:"title"`
	StartSeconds       float64 `json:"start_seconds"`
	EndSeconds         float64 `json:"end_seconds"`
	Source             string  `json:"source" doc:"Where the chapter came from"`
	ThumbnailURL       string  `json:"thumbnail_url,omitempty"`
	ThumbnailThumbhash string  `json:"thumbnail_thumbhash,omitempty"`
}

// WatchMarker is a skippable span.
type WatchMarker struct {
	StartSeconds float64 `json:"start_seconds" example:"0"`
	EndSeconds   float64 `json:"end_seconds" example:"90"`
}

// WatchPlaybackVariant is one logical watch choice.
type WatchPlaybackVariant struct {
	VariantID            string                     `json:"variant_id"`
	EditionRaw           string                     `json:"edition_raw,omitempty"`
	EditionKey           string                     `json:"edition_key,omitempty"`
	PresentationKind     string                     `json:"presentation_kind,omitempty"`
	PresentationGroupKey string                     `json:"presentation_group_key,omitempty"`
	PartCount            int                        `json:"part_count"`
	TotalDurationSeconds int                        `json:"total_duration_seconds,omitempty"`
	DefaultFileID        ID                         `json:"default_file_id,omitempty"`
	Parts                []WatchPlaybackVariantPart `json:"parts" doc:"Ordered; empty, never null"`
}

// WatchPlaybackVariantPart holds the interchangeable versions of one part.
type WatchPlaybackVariantPart struct {
	PartIndex            int                `json:"part_index"`
	DefaultFileID        ID                 `json:"default_file_id,omitempty"`
	TotalDurationSeconds int                `json:"total_duration_seconds,omitempty"`
	Versions             []WatchFileVersion `json:"versions" doc:"Empty, never null"`
}

// WatchSubtitle is a subtitle available for the item.
type WatchSubtitle struct {
	Source          string `json:"source" doc:"embedded or external" example:"embedded"`
	Language        string `json:"language" example:"eng"`
	Codec           string `json:"codec,omitempty"`
	Forced          bool   `json:"forced"`
	HearingImpaired bool   `json:"hearing_impaired"`
	Title           string `json:"title,omitempty"`
}

// WatchUserData is the acting profile's progress on the item. For an ebook
// position/duration encode the 0..1 reading ratio (position=ratio,
// duration=1), as v1 does.
type WatchUserData struct {
	PositionSeconds float64 `json:"position_seconds,omitempty" example:"1325.5"`
	DurationSeconds float64 `json:"duration_seconds,omitempty" example:"10200"`
	IsInProgress    bool    `json:"is_in_progress,omitempty"`
	WatchedCount    int     `json:"watched_count"`
	UnplayedCount   int     `json:"unplayed_count"`
	InProgressCount int     `json:"in_progress_count"`
	Played          bool    `json:"played"`
	LastFileID      ID      `json:"last_file_id,omitempty" doc:"The version last played"`
	LastResolution  *string `json:"last_resolution,omitempty"`
	LastHDR         *bool   `json:"last_hdr,omitempty"`
	LastCodecVideo  *string `json:"last_codec_video,omitempty"`
	LastEditionKey  *string `json:"last_edition_key,omitempty"`
}

// WatchSubtitleSignature identifies a subtitle track independent of index.
type WatchSubtitleSignature struct {
	Source          string `json:"source,omitempty"`
	Language        string `json:"language,omitempty"`
	Codec           string `json:"codec,omitempty"`
	Label           string `json:"label,omitempty"`
	Forced          bool   `json:"forced"`
	HearingImpaired bool   `json:"hearing_impaired"`
}

const (
	locationPathID    = "path.id"
	locationQueryFile = "query.file_id"
)

// WatchedInput names the item to mark.
type WatchedInput struct {
	ID ID `path:"id" doc:"A movie, ebook, episode, season or series; a season or series expands to its episodes" example:"movie:heat-1995"`
}

func registerWatch(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/watch/{id}", "getWatchState", "watch",
			"Get what is needed to play an item: its versions, subtitles, markers and the acting profile's progress."),
		Class: ClassProfileScoped,
		// Profile scoped without a required header, as v1 GET /watch/{id}:
		// the catalog answer needs only the account's access; the profile,
		// when declared, adds its progress and preferences.
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.getWatchState)

	mark := humaOp(http.MethodPost, Prefix+"/watched/{id}", "markWatched", "watch",
		"Mark an item watched for the acting profile; a season or series marks every episode. Marking an already watched item is a no-op.")
	mark.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: mark, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}, func(ctx context.Context, in *WatchedInput) (*struct{}, error) {
		return reg.setWatched(ctx, in, true)
	})

	unmark := humaOp(http.MethodDelete, Prefix+"/watched/{id}", "unmarkWatched", "watch",
		"Mark an item unwatched for the acting profile; a season or series clears every episode. The server chooses the history cutoff when this request runs.")
	unmark.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: unmark, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}, func(ctx context.Context, in *WatchedInput) (*struct{}, error) {
		return reg.setWatched(ctx, in, false)
	})
}

// getWatchState answers the same detail as v1 GET /watch/{id}. A series (not
// directly playable) is a validation failure naming the path parameter.
func (reg *Registry) getWatchState(ctx context.Context, in *WatchDetailInput) (*WatchDetailOutput, error) {
	if reg.deps.Watch == nil {
		return nil, unavailable("watch")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	opts := handlers.AccessFilterOptions{}
	if in.FileID != "" {
		n, err := intOfID(in.FileID)
		if err != nil || n <= 0 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationQueryFile, Code: codeInvalid, Detail: "file_id must name a file."})
		}
		opts.SelectedFileID = n
	}
	if in.LibraryID != "" {
		n, err := intOfID(in.LibraryID)
		if err != nil || n <= 0 {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationQueryLibraryID, Code: codeInvalid, Detail: "library_id must name a library."})
		}
		opts.PresentationLibraryID = &n
	}
	if size, err := imagesize.Parse(in.ImageSize); err == nil {
		opts.ImageSize = size
	}
	filter, err := reg.deps.Watch.ContextAccessFilter(ctx, opts)
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	detail, err := reg.deps.Watch.WatchDetail(ctx, claims.UserID, profileFrom(ctx), string(in.ID), filter)
	if err != nil {
		var apiErr *handlers.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "invalid_watch_target" {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationPathID, Code: codeInvalid, Detail: apiErr.Message})
		}
		return nil, serviceProblem(err)
	}
	return &WatchDetailOutput{Body: watchDetailOf(detail)}, nil
}

// setWatched runs the same command as v1 POST/DELETE /watched/{id}.
func (reg *Registry) setWatched(ctx context.Context, in *WatchedInput, played bool) (*struct{}, error) {
	if reg.deps.Watch == nil {
		return nil, unavailable("watch")
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	filter, err := reg.deps.Watch.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	if _, err := reg.deps.Watch.SetWatchedState(ctx, userID, profileID, string(in.ID), played, filter); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func watchDetailOf(d *catalogpkg.WatchDetail) WatchDetail {
	out := WatchDetail{
		ContentID:                  d.ContentID,
		Type:                       d.Type,
		Title:                      d.Title,
		Year:                       d.Year,
		Overview:                   d.Overview,
		Versions:                   watchVersionsOf(d.Versions),
		PlaybackVariants:           watchVariantsOf(d.PlaybackVariants),
		Subtitles:                  make([]WatchSubtitle, 0, len(d.Subtitles)),
		Intro:                      watchMarkerOf(d.Intro),
		Credits:                    watchMarkerOf(d.Credits),
		Recap:                      watchMarkerOf(d.Recap),
		Preview:                    watchMarkerOf(d.Preview),
		UserData:                   watchUserDataOf(d.UserData),
		SeriesID:                   d.SeriesID,
		SeriesTitle:                d.SeriesTitle,
		SeasonNumber:               d.SeasonNumber,
		EpisodeNumber:              d.EpisodeNumber,
		EffectiveVersionResolution: d.EffectiveVersionResolution,
		EffectiveVersionHDR:        d.EffectiveVersionHDR,
		EffectiveVersionCodecVideo: d.EffectiveVersionCodecVideo,
		EffectiveVersionEditionKey: d.EffectiveVersionEditionKey,
	}
	for _, s := range d.Subtitles {
		out.Subtitles = append(out.Subtitles, WatchSubtitle{Source: s.Source, Language: s.Language, Codec: s.Codec, Forced: s.Forced, HearingImpaired: s.HearingImpaired, Title: s.Title})
	}
	if d.HasEffectiveSubtitleLang {
		out.EffectiveSubtitleLanguage = ptr(d.EffectiveSubtitleLanguage)
	}
	if d.HasEffectiveSubtitleMode {
		out.EffectiveSubtitleMode = ptr(d.EffectiveSubtitleMode)
	}
	if d.HasEffectiveShowForcedSubtitles {
		out.EffectiveShowForcedSubtitles = ptr(d.EffectiveShowForcedSubtitles)
	}
	if sig := d.EffectiveSubtitleTrackSignature; sig != nil {
		out.EffectiveSubtitleTrackSignature = watchSignatureOf(*sig)
	}
	return out
}

func watchSignatureOf(s userstore.SubtitleTrackSignature) *WatchSubtitleSignature {
	return &WatchSubtitleSignature{Source: s.Source, Language: s.Language, Codec: s.Codec, Label: s.Label, Forced: s.Forced, HearingImpaired: s.HearingImpaired}
}

func watchVersionsOf(vs []catalogpkg.FileVersion) []WatchFileVersion {
	out := make([]WatchFileVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, watchVersionOf(v))
	}
	return out
}

func watchVersionOf(v catalogpkg.FileVersion) WatchFileVersion {
	out := WatchFileVersion{
		FileID:                   idOfInt(v.FileID),
		FileName:                 v.FileName,
		FilePath:                 v.FilePath,
		Resolution:               v.Resolution,
		CodecVideo:               v.CodecVideo,
		CodecAudio:               v.CodecAudio,
		HDR:                      v.HDR,
		Container:                v.Container,
		FileSize:                 v.FileSize,
		DurationSeconds:          v.Duration,
		Bitrate:                  v.Bitrate,
		AddedAt:                  NewInstant(v.AddedAt),
		EditionRaw:               v.EditionRaw,
		EditionKey:               v.EditionKey,
		PresentationKind:         v.PresentationKind,
		PresentationGroupKey:     v.PresentationGroupKey,
		PresentationPartIndex:    v.PresentationPartIndex,
		PresentationPartTotal:    v.PresentationPartTotal,
		MultiEpisodeStart:        v.MultiEpisodeStart,
		MultiEpisodeEnd:          v.MultiEpisodeEnd,
		EffectiveAudioTrackIndex: v.EffectiveAudioTrackIndex,
		EffectiveAudioLanguage:   v.EffectiveAudioLanguage,
		Intro:                    watchMarkerOf(v.Intro),
		Credits:                  watchMarkerOf(v.Credits),
		Recap:                    watchMarkerOf(v.Recap),
		Preview:                  watchMarkerOf(v.Preview),
	}
	for _, t := range v.VideoTracks {
		out.VideoTracks = append(out.VideoTracks, watchVideoTrackOf(t))
	}
	for _, t := range v.AudioTracks {
		out.AudioTracks = append(out.AudioTracks, WatchAudioTrack{Title: t.Title, EmbeddedTitle: t.EmbeddedTitle, Language: t.Language, Codec: t.Codec, Profile: t.Profile, Layout: t.Layout, Channels: t.Channels, Bitrate: t.Bitrate, SampleRate: t.SampleRate, BitDepth: t.BitDepth, Default: t.Default})
	}
	for _, t := range v.SubtitleTracks {
		out.SubtitleTracks = append(out.SubtitleTracks, WatchSubtitleTrack{Index: t.Index, Language: t.Language, Codec: t.Codec, Title: t.Title, EmbeddedTitle: t.EmbeddedTitle, Resolution: t.Resolution, Forced: t.Forced, Default: t.Default, HearingImpaired: t.HearingImpaired, External: t.External, FileName: t.FileName})
	}
	for _, c := range v.Chapters {
		out.Chapters = append(out.Chapters, WatchChapter{Index: c.Index, Title: c.Title, StartSeconds: c.StartSeconds, EndSeconds: c.EndSeconds, Source: c.Source, ThumbnailURL: c.ThumbnailURL, ThumbnailThumbhash: c.ThumbnailThumbhash})
	}
	return out
}

func watchVideoTrackOf(t models.VideoTrack) WatchVideoTrack {
	return WatchVideoTrack{
		Title: t.Title, Codec: t.Codec, DolbyVision: t.DolbyVision, DVProfile: t.DVProfile, DVLevel: t.DVLevel,
		DVBLCompatID: t.DVBLCompatID, DVConfigPresent: t.DVConfigPresent, DVBLCompatIDPresent: t.DVBLCompatIDPresent,
		DVBLPresent: t.DVBLPresent, DVRPUPresent: t.DVRPUPresent, DVELPresent: t.DVELPresent, DVEnhancementLayer: t.DVEnhancementLayer,
		HDR10Plus: t.HDR10Plus, Profile: t.Profile, Level: t.Level, Width: t.Width, Height: t.Height, AspectRatio: t.AspectRatio,
		Interlaced: t.Interlaced, FrameRate: t.FrameRate, Bitrate: t.Bitrate, VideoRange: t.VideoRange, VideoRangeType: t.VideoRangeType,
		ColorRange: t.ColorRange, ColorPrimaries: t.ColorPrimaries, ColorSpace: t.ColorSpace, ColorTransfer: t.ColorTransfer,
		BitDepth: t.BitDepth, PixelFormat: t.PixelFormat, ReferenceFrames: t.ReferenceFrames,
	}
}

func watchVariantsOf(vs []catalogpkg.PlaybackVariant) []WatchPlaybackVariant {
	if len(vs) == 0 {
		return nil
	}
	out := make([]WatchPlaybackVariant, 0, len(vs))
	for _, v := range vs {
		parts := make([]WatchPlaybackVariantPart, 0, len(v.Parts))
		for _, p := range v.Parts {
			parts = append(parts, WatchPlaybackVariantPart{PartIndex: p.PartIndex, DefaultFileID: optionalIDOfInt(p.DefaultFileID), TotalDurationSeconds: p.TotalDuration, Versions: watchVersionsOf(p.Versions)})
		}
		out = append(out, WatchPlaybackVariant{
			VariantID: v.VariantID, EditionRaw: v.EditionRaw, EditionKey: v.EditionKey, PresentationKind: v.PresentationKind,
			PresentationGroupKey: v.PresentationGroupKey, PartCount: v.PartCount, TotalDurationSeconds: v.TotalDuration,
			DefaultFileID: optionalIDOfInt(v.DefaultFileID), Parts: parts,
		})
	}
	return out
}

func watchMarkerOf(m *catalogpkg.Marker) *WatchMarker {
	if m == nil {
		return nil
	}
	return &WatchMarker{StartSeconds: m.Start, EndSeconds: m.End}
}

func watchUserDataOf(u *catalogpkg.SeasonUserData) *WatchUserData {
	if u == nil {
		return nil
	}
	out := &WatchUserData{
		PositionSeconds: u.PositionSeconds, DurationSeconds: u.DurationSeconds, IsInProgress: u.IsInProgress,
		WatchedCount: u.WatchedCount, UnplayedCount: u.UnplayedCount, InProgressCount: u.InProgressCount, Played: u.Played,
		LastResolution: u.LastResolution, LastHDR: u.LastHDR, LastCodecVideo: u.LastCodecVideo, LastEditionKey: u.LastEditionKey,
	}
	if u.LastFileID != nil {
		out.LastFileID = optionalIDOfInt(*u.LastFileID)
	}
	return out
}

// idOfInt renders a numeric database key as the opaque wire ID.
func idOfInt(n int) ID { return ID(strconv.Itoa(n)) }

// optionalIDOfInt renders a numeric key that is absent when zero.
func optionalIDOfInt(n int) ID {
	if n == 0 {
		return ""
	}
	return idOfInt(n)
}

// WatchService is the slice of *handlers.ItemsHandler the watch operations
// use.
type WatchService interface {
	ContextAccessFilter(ctx context.Context, opts handlers.AccessFilterOptions) (catalogpkg.AccessFilter, error)
	WatchDetail(ctx context.Context, userID int, profileID, contentID string, filter catalogpkg.AccessFilter) (*catalogpkg.WatchDetail, error)
	SetWatchedState(ctx context.Context, userID int, profileID, contentID string, played bool, filter catalogpkg.AccessFilter) (handlers.WatchedStateView, error)
}
