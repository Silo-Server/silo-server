package models

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	mediaBaseTypeAudiobook = "audiobook"
	mediaBaseTypePodcast   = "podcast"
	mediaCodecMJPEG        = "mjpeg"
)

// MediaFolder represents a row in the media_folders table.
type MediaFolder struct {
	ID                       int
	Paths                    []string // from media_folder_paths child table
	Type                     string   // movies, series, mixed
	Name                     string
	Enabled                  bool
	MetadataLanguage         string // ISO 639-1 code (e.g. "en", "ja")
	AutoTranslateMetadata    bool   // AI-translate descriptions when providers lack this language
	ChapterThumbnailsEnabled bool
	IntroDetectionEnabled    bool
	// TrickplayEnabled generates seek-bar previews for the library's video
	// files (internal/trickplay). Off by default.
	TrickplayEnabled bool
	// BoxSetsEnabled browses the movies TMDB lists in one collection as a
	// single box set card once at least BoxSetMinMovies of them are in the
	// library. BoxSetsInSearch also lists matching box sets in search.
	BoxSetsEnabled  bool
	BoxSetMinMovies int
	BoxSetsInSearch bool
	// RealtimeMonitoring is the library's real-time monitoring switch. It
	// takes effect only while the server-wide scanner.realtime_monitoring
	// setting is on and the library is enabled.
	RealtimeMonitoring bool
	// TrailerKinds is the allow-list of remote video kinds (ExtraKind values)
	// fetched during metadata refresh for this library. Empty disables remote
	// videos entirely.
	TrailerKinds          []string
	PosterPath            string     // S3 key for library poster image
	LastScannedAt         *time.Time // nullable
	ScanWarningCode       *string
	ScanWarningMessage    *string
	ScanWarningAt         *time.Time
	AllowEmptyCleanupOnce bool
	SortOrder             int
}

// VirtualProvenance is the explicit origin discriminator for virtual media.
// It answers "which subsystem owns this virtual row" without redefining
// owner zero: zero still means local (see MediaFile.VirtualOwnerInstallationID),
// and the legacy-0-vs-NULL ambiguity stays with VirtualOwnerInstallationSet.
// The zero value ("") means legacy/unknown and follows the pre-existing plugin
// path; only the NEW core dispatch fails closed on unknown/inconsistent
// provenance. No DB column backs this in Phase 2 — struct-level only.
type VirtualProvenance string

const (
	VirtualProvenanceLocal  VirtualProvenance = "local"
	VirtualProvenanceCore   VirtualProvenance = "core"
	VirtualProvenancePlugin VirtualProvenance = "plugin"
)

// MediaFile represents a row in the media_files table.
type MediaFile struct {
	ID                          int
	ContentID                   string // Sonyflake ID (nullable until matched)
	EpisodeID                   string // FK to episodes.content_id (nullable)
	ExtraID                     string // FK to media_extras.content_id (nullable); set only for local extras files, which keep ContentID/EpisodeID empty
	SeasonNumber                int    // parsed from filename (nullable)
	EpisodeNumber               int    // parsed from filename (nullable)
	MediaFolderID               int
	CanonicalRootPath           string
	ObservedRootPath            string
	ContentGroupKey             string
	GroupKeyVersion             int
	BaseTitle                   string
	BaseYear                    int
	BaseType                    string
	IdentityConfidence          string
	IdentityJSON                []byte
	FilePath                    string
	VirtualOwnerInstallationID  int  // owner for zero-storage virtual files; zero for local files
	VirtualOwnerInstallationSet bool // distinguishes legacy virtual owner 0 from local NULL
	// VirtualProvenance is the explicit origin discriminator (core/plugin/
	// local/""). In-memory only: rows loaded from storage carry "" until
	// resolved by ResolvedVirtualProvenance(), which infers core provenance
	// for virtual files with owner <= 0 across database reloads.
	VirtualProvenance            VirtualProvenance
	FileSize                     int64
	FileModifiedAt               *time.Time
	FileHash                     string // OSHash (16-char hex)
	CodecVideo                   string // h264, hevc, av1
	CodecAudio                   string // aac, opus, flac
	Resolution                   string // 1080p, 2160p
	AudioChannels                int
	HDR                          bool
	Container                    string             // mkv, mp4
	Duration                     int                // seconds
	Bitrate                      int                // kbps
	VideoTracks                  []VideoTrack       // JSONB
	AudioTracks                  []AudioTrack       // JSONB
	SubtitleTracks               []SubtitleTrack    // JSONB
	ExternalSubtitles            []ExternalSubtitle // JSONB
	Chapters                     []MediaChapter     // JSONB; nil means not yet probed for chapters
	MarkerThumbnails             []MarkerThumbnail  `json:"-"`
	ChapterThumbnailRetryAfter   *time.Time
	ChapterThumbnailFailureCount int
	ChapterThumbnailLastError    string
	IntroStart                   *float64
	IntroEnd                     *float64
	CreditsStart                 *float64
	CreditsEnd                   *float64
	RecapStart                   *float64
	RecapEnd                     *float64
	PreviewStart                 *float64
	PreviewEnd                   *float64
	MarkerSegments               []MarkerSegment // JSONB; legacy bounds expose the first occurrence per kind
	MarkersSource                *string
	MarkersConfidence            *float64
	IntroMarkersSource           *string
	IntroMarkersProvider         *string
	IntroMarkersConfidence       *float64
	IntroMarkersAlgorithm        *string
	IntroMarkersDetectedAt       *time.Time
	CreditsMarkersSource         *string
	CreditsMarkersProvider       *string
	CreditsMarkersConfidence     *float64
	CreditsMarkersAlgorithm      *string
	CreditsMarkersDetectedAt     *time.Time
	RecapMarkersSource           *string
	RecapMarkersProvider         *string
	RecapMarkersConfidence       *float64
	RecapMarkersAlgorithm        *string
	RecapMarkersDetectedAt       *time.Time
	PreviewMarkersSource         *string
	PreviewMarkersProvider       *string
	PreviewMarkersConfidence     *float64
	PreviewMarkersAlgorithm      *string
	PreviewMarkersDetectedAt     *time.Time
	EditionRaw                   string
	EditionKey                   string
	EditionConfidence            *float64
	EditionSource                string
	// ReleaseName is the file stem (basename without extension), the closest
	// the server gets to the release's advertised name.
	ReleaseName string
	// ReleaseGroup is the trailing group tag on release-style names
	// ("Movie.2023.2160p.AltMount" → "AltMount"), "" when none is present.
	ReleaseGroup          string
	PresentationKind      string
	PresentationGroupKey  string
	PresentationPartIndex int
	PresentationPartTotal int
	MultiEpisodeStart     int
	MultiEpisodeEnd       int
	// MultiplePPS is the persisted H.264 multi-PPS copy-safety verdict; nil
	// means the file has never been successfully analyzed. It is trusted only
	// when MultiplePPSScanSize and MultiplePPSScanMtime still match the file's
	// current size and mtime, so a rewritten file self-invalidates without any
	// coordination from the writers that touch media_files.
	//
	// json:"-" on all three: MediaFile is not a client-facing shape, and the
	// runtime copy-safety signal clients do act on lives on VideoTrack.
	MultiplePPS          *bool      `json:"-"`
	MultiplePPSScanSize  *int64     `json:"-"`
	MultiplePPSScanMtime *time.Time `json:"-"`
	ProbeSource          string     // arrs, local
	ProbeUpdatedAt       *time.Time
	// RefusedProbeEvidence marks an in-memory inventory override assembled from a
	// probe whose catalog write was refused. The tracks are real measured
	// evidence, but no durable write committed them, so no reader may report the
	// override as verified. It is never persisted and never serialized: catalog
	// rows loaded from storage leave it false.
	RefusedProbeEvidence bool `json:"-"`
	// ProbedFormatSize is ffprobe's format.size for the bytes a virtual probe
	// actually read, in bytes. It is in-memory only and never serialized: the
	// playback probe path compares it against the row's declared FileSize to
	// detect a provider serving a different release under the row's URL. Zero
	// means the probe did not report a size (or the row was never probed).
	ProbedFormatSize int64 `json:"-"`
	// VirtualExpectedRuntimeMinutes is the catalog runtime, in minutes, of the
	// item whose bytes a playback-time probe is validating. It is in-memory
	// only and never serialized: the playback handler stamps it on the probe
	// input so the probe cache can accept a short-posted copy of the right
	// content when its measured duration matches. Zero means no expectation is
	// known, and a served-size mismatch stays a rejection.
	VirtualExpectedRuntimeMinutes int `json:"-"`
	// ProbeVersion is the schema version of the probe-derived columns
	// (audio/subtitle track shape, track languages). Scans bump it so rows
	// probed before a probe-shape change are re-probed once instead of being
	// served forever with the legacy shape.
	ProbeVersion     int
	ProbeFailedAt    *time.Time // last local ffprobe rejected the file; cleared by a successful probe (see ProbeRejected)
	MatchAttemptedAt *time.Time
	MissingSince     *time.Time
	// FailedAt marks a virtual candidate that produced no bytes at
	// stream-open (corrupted NZB, dead provider URL). A fresh listing clears
	// it; the auto-pick skips failed candidates while the dropdown still
	// shows them for a manual retry.
	FailedAt *time.Time
	// LastDeliveredAt is the last time this virtual candidate delivered media
	// bytes to a client. It is the durable known-good evidence the delivery
	// grace and the optimistic start path read; nil means never delivered.
	LastDeliveredAt *time.Time
	// ResolvedURL is the provider stream URL this virtual candidate last
	// successfully resolved to. It is purely additive to the neutral
	// `virtual://...?result=<id>` file_path, which remains the row's listing
	// identity: a provider re-list churns result ids, so this column lets a
	// later phase reuse the last known-good URL instead of re-listing. Empty
	// means the row has never resolved. ResolvedURLExpiresAt is the URL's
	// parsed expiry (see stream.ParseStreamDetails); nil when the URL carries
	// no parseable expiry.
	//
	// The Provider* fields are the candidate's durable identity in the same
	// tier order as the dedup key: video hash, then source GUID, then release
	// name + size. They let a row be re-matched to a fresh listing after the
	// provider rotates result ids.
	ResolvedURL          string     `json:"-"`
	ResolvedURLExpiresAt *time.Time `json:"-"`
	ProviderVideoHash    string     `json:"-"`
	ProviderGUID         string     `json:"-"`
	ProviderReleaseName  string     `json:"-"`
	ProviderReleaseSize  int64      `json:"-"`
	// ProviderRequestHeaders is the request header set the provider stream URL
	// needs (the relay forwards Referer/Origin/User-Agent from
	// behaviorHints.proxyHeaders). It is stored alongside ResolvedURL so a
	// header-authenticated URL is usable when served from the catalog instead
	// of a fresh listing. Nil means the row carries no headers.
	ProviderRequestHeaders map[string]string `json:"-"`
	FirstSeenScanRunID     string
	CreatedAt              time.Time
	UpdatedAt              time.Time

	// Non-nil only for an ephemeral on-demand overlay; fences canonical edits.
	MarkerThumbnailBaseSegments []MarkerSegment `json:"-"`
}

// MediaChapter represents a single media chapter derived from embedded file metadata.
type MediaChapter struct {
	Index               int        `json:"index"`
	Title               string     `json:"title"`
	StartSeconds        float64    `json:"start_seconds"`
	EndSeconds          float64    `json:"end_seconds"`
	Source              string     `json:"source"`
	ThumbnailPath       string     `json:"thumbnail_path,omitempty"`
	ThumbnailThumbhash  string     `json:"thumbnail_thumbhash,omitempty"`
	ThumbnailRetryAfter *time.Time `json:"thumbnail_retry_after,omitempty"`
	ThumbnailFailedAt   *time.Time `json:"thumbnail_failed_at,omitempty"`
	ThumbnailLastError  string     `json:"thumbnail_last_error,omitempty"`
}

// OverlaySummary is the compact media badge payload shared across API surfaces.
type OverlaySummary struct {
	Resolution    string `json:"resolution,omitempty"`
	HDR           string `json:"hdr,omitempty"`
	Audio         string `json:"audio,omitempty"`
	AudioChannels string `json:"audio_channels,omitempty"` // "Stereo", "5.1", "7.1"
	VideoCodec    string `json:"video_codec,omitempty"`    // "H.264", "H.265", "AV1"
	Container     string `json:"container,omitempty"`      // "MKV", "MP4"
	AspectRatio   string `json:"aspect_ratio,omitempty"`   // "16:9", "2.39:1"
	ReleaseType   string `json:"release_type,omitempty"`
	Edition       string `json:"edition,omitempty"`
	MultiAudio    bool   `json:"multi_audio,omitempty"` // ≥2 distinct audio languages
	MultiSub      bool   `json:"multi_sub,omitempty"`   // ≥1 subtitle track (embedded or external)
}

// ResolvedVirtualProvenance returns the effective VirtualProvenance for this
// file, inferring it across database reloads when the in-memory field is
// unset. If explicitly set, that value wins. Otherwise, if the file is virtual
// (Container=="virtual" or FilePath starts with "virtual://"):
//   - VirtualOwnerInstallationID <= 0 implies core provenance;
//   - VirtualOwnerInstallationID > 0 implies plugin provenance.
//
// Non-virtual files return VirtualProvenanceLocal.
func (f *MediaFile) ResolvedVirtualProvenance() VirtualProvenance {
	if f == nil {
		return ""
	}
	if f.VirtualProvenance != "" {
		return f.VirtualProvenance
	}
	isVirtual := f.Container == "virtual" || strings.HasPrefix(strings.ToLower(f.FilePath), "virtual://")
	if !isVirtual {
		return VirtualProvenanceLocal
	}
	if f.VirtualOwnerInstallationID <= 0 {
		return VirtualProvenanceCore
	}
	return VirtualProvenancePlugin
}

// PrimaryDVProfile returns the Dolby Vision profile of the first video
// track (0 when none/unprobed).
func (f *MediaFile) PrimaryDVProfile() int {
	if f == nil || len(f.VideoTracks) == 0 {
		return 0
	}
	return f.VideoTracks[0].DVProfile
}

// AudioOnlyProbeFacts is the compact probe shape needed to distinguish known
// audio media from incomplete video probes and legacy attached cover art.
type AudioOnlyProbeFacts struct {
	BaseType               string
	CodecVideo             string
	CodecAudio             string
	HasVideoTracks         bool
	HasAudioTracks         bool
	HasNonImageVideoTracks bool
}

func isImageVideoCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case mediaCodecMJPEG, "jpeg", "png", "webp", "gif", "bmp":
		return true
	default:
		return false
	}
}

// HasLegacyAttachedPictureVideo reports the stale shape in which every video
// track on known audio media is embedded cover art.
func (f AudioOnlyProbeFacts) HasLegacyAttachedPictureVideo() bool {
	knownAudioType := f.BaseType == mediaBaseTypeAudiobook || f.BaseType == mediaBaseTypePodcast
	hasAudio := f.HasAudioTracks || strings.TrimSpace(f.CodecAudio) != ""
	if !knownAudioType || !hasAudio {
		return false
	}
	if !f.HasVideoTracks {
		return isImageVideoCodec(f.CodecVideo)
	}
	if f.HasNonImageVideoTracks {
		return false
	}
	return strings.TrimSpace(f.CodecVideo) == "" || isImageVideoCodec(f.CodecVideo)
}

// IsAudioOnly reports whether these facts contain positive evidence for known
// audio media with no playable video stream.
func (f AudioOnlyProbeFacts) IsAudioOnly() bool {
	knownAudioType := f.BaseType == mediaBaseTypeAudiobook || f.BaseType == mediaBaseTypePodcast
	hasAudio := f.HasAudioTracks || strings.TrimSpace(f.CodecAudio) != ""
	return (knownAudioType && hasAudio && !f.HasVideoTracks && strings.TrimSpace(f.CodecVideo) == "") || f.HasLegacyAttachedPictureVideo()
}

// AudioOnlyProbeFacts returns the compact stream evidence for this media file.
func (f *MediaFile) AudioOnlyProbeFacts() AudioOnlyProbeFacts {
	if f == nil {
		return AudioOnlyProbeFacts{}
	}
	facts := AudioOnlyProbeFacts{
		BaseType:       f.BaseType,
		CodecVideo:     f.CodecVideo,
		CodecAudio:     f.CodecAudio,
		HasVideoTracks: len(f.VideoTracks) > 0,
		HasAudioTracks: len(f.AudioTracks) > 0,
	}
	for _, track := range f.VideoTracks {
		if !isImageVideoCodec(track.Codec) {
			facts.HasNonImageVideoTracks = true
			break
		}
	}
	return facts
}

// HasLegacyAttachedPictureVideo reports the stale catalog shape produced when
// older probes recorded embedded cover art as a video track. BaseType and
// audio evidence keep genuine MJPEG video from being normalized away.
func (f *MediaFile) HasLegacyAttachedPictureVideo() bool {
	return f.AudioOnlyProbeFacts().HasLegacyAttachedPictureVideo()
}

// ProbeRejected reports whether ffprobe rejected this file and no usable
// stream metadata exists for it: no successful probe has been recorded and
// nothing describes a video or audio stream. Such a file cannot be played until
// it is replaced, as opposed to a file that has simply not been probed yet.
// Rows that still carry stream metadata from an earlier probe or an import are
// not rejected, so a transient probe failure never hides a known-good source.
func (f *MediaFile) ProbeRejected() bool {
	if f == nil || f.ProbeFailedAt == nil || f.ProbeUpdatedAt != nil {
		return false
	}
	return strings.TrimSpace(f.CodecVideo) == "" && strings.TrimSpace(f.CodecAudio) == "" &&
		len(f.VideoTracks) == 0 && len(f.AudioTracks) == 0
}

// IsAudioOnly reports whether a probed file carries no playable video stream —
// audiobooks and podcasts, as opposed to a video file whose probe is incomplete.
// It also normalizes legacy audiobook/podcast cover-art rows until a repair
// probe removes their stale image-only video metadata.
func (f *MediaFile) IsAudioOnly() bool {
	return f.AudioOnlyProbeFacts().IsAudioOnly()
}

// NormalizeVideoBitDepth returns an explicit probe value when available and
// otherwise derives the bit depth from stable ffprobe fields. FFprobe commonly
// omits bits_per_raw_sample for HEVC even though the pixel format and profile
// are conclusive (for example yuv420p10le / Main 10).
func NormalizeVideoBitDepth(explicit int, pixelFormat, profile string) int {
	if explicit > 0 {
		return explicit
	}

	pixelFormat = strings.ToLower(strings.TrimSpace(pixelFormat))
	for _, candidate := range []struct {
		markers []string
		depth   int
	}{
		{markers: []string{"p016", "p16", "gray16", "16le", "16be"}, depth: 16},
		{markers: []string{"p014", "p14", "gray14", "14le", "14be"}, depth: 14},
		{markers: []string{"p012", "p12", "gray12", "12le", "12be"}, depth: 12},
		{markers: []string{"p010", "p10", "gray10", "10le", "10be"}, depth: 10},
		{markers: []string{"p009", "p9", "gray9", "9le", "9be"}, depth: 9},
	} {
		for _, marker := range candidate.markers {
			if strings.Contains(pixelFormat, marker) {
				return candidate.depth
			}
		}
	}

	profile = strings.ToLower(strings.TrimSpace(profile))
	for _, marker := range []string{"main 12", "main12", "12-bit", "12 bit"} {
		if strings.Contains(profile, marker) {
			return 12
		}
	}
	for _, marker := range []string{"main 10", "main10", "10-bit", "10 bit"} {
		if strings.Contains(profile, marker) {
			return 10
		}
	}

	switch pixelFormat {
	case "yuv420p", "yuv422p", "yuv444p", "yuvj420p", "yuvj422p", "yuvj444p", "nv12", "nv21", "rgb24", "bgr24":
		return 8
	}
	return 0
}

// VideoTrack represents a probed video stream stored as JSONB.
type VideoTrack struct {
	Title               string `json:"title,omitempty"`
	Codec               string `json:"codec,omitempty"`
	DolbyVision         string `json:"dolby_vision,omitempty"`
	DVProfile           int    `json:"dv_profile,omitempty"`
	DVLevel             int    `json:"dv_level,omitempty"`
	DVBLCompatID        int    `json:"dv_bl_compat_id,omitempty"`
	DVConfigPresent     bool   `json:"dv_config_present"`
	DVBLCompatIDPresent bool   `json:"dv_bl_compat_id_present"`
	// DVProvenanceCurrent records whether stored JSON explicitly carried both
	// provenance booleans. Nil denotes an in-memory track that has not crossed
	// the storage boundary; false identifies legacy rows written by old nodes.
	DVProvenanceCurrent *bool  `json:"-"`
	DVBLPresent         bool   `json:"dv_bl_present,omitempty"`
	DVRPUPresent        bool   `json:"dv_rpu_present,omitempty"`
	DVELPresent         bool   `json:"dv_el_present,omitempty"`
	DVEnhancementLayer  string `json:"dv_enhancement_layer,omitempty"` // none, mel, fel, unknown
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
	// FrameColorRange is the range FFmpeg reported on the primary PQ track's
	// first decoded frame, "unknown" when the frame carried none. The decoder
	// takes it from the bitstream, which can contradict the container's tag in
	// ColorRange, and tone-map graphs see the decoder's value. It is empty
	// when the frame was not read.
	FrameColorRange string `json:"frame_color_range,omitempty" doc:"Color range FFmpeg reported on the first decoded frame of a PQ track (tv, pc, or unknown when the frame carried none); absent when the frame was not read. Can differ from color_range, the container's tag"`
	ColorPrimaries  string `json:"color_primaries,omitempty"`
	ColorSpace      string `json:"color_space,omitempty"`
	ColorTransfer   string `json:"color_transfer,omitempty"`
	BitDepth        int    `json:"bit_depth,omitempty"`
	PixelFormat     string `json:"pixel_format,omitempty"`
	ReferenceFrames int    `json:"reference_frames,omitempty"`
	// MultiplePPS records whether an H.264 stream redefines the same
	// pic_parameter_set_id in-band with more than one distinct content. Such
	// streams cannot be safely stream-copied into an avc1/fMP4 HLS segment:
	// the avcC declares a single parameter set, so strict decoders
	// (VideoToolbox on Safari/Chrome-macOS) desync on the mid-GOP switches.
	//
	// This is a runtime-only field: it is computed at playback start by a
	// bitstream scan and held in memory, never serialized to the database
	// (`json:"-"`). nil means "not analyzed in this process yet".
	MultiplePPS *bool `json:"-"`
	// VideoCopyUnsafe is set when conflicting PPS data is detected or when the
	// safety scan cannot establish that video stream-copy is safe. It is
	// runtime-only so transient scan failures are retried on a later request.
	VideoCopyUnsafe bool `json:"-"`
	// DVRPUStrippable is the runtime verdict for removing Dolby Vision RPUs
	// from this exact source. It is populated for resolved virtual streams,
	// whose loopback relay cannot be os.Stat'ed by the local-file probe.
	DVRPUStrippable *bool `json:"-"`
	// HDR10PlusUnchecked records that the track's HDR10+ status was never
	// read from a decoded frame: it was probed before frame-level detection,
	// by playback's quick repair, or when the frame read failed transiently.
	// MarshalJSON writes hdr10_plus only for checked tracks, so a stored PQ
	// track without the key is checked by the HDR10+ backfill task.
	HDR10PlusUnchecked bool `json:"-"`
}

// UnmarshalJSON preserves raw-key presence so rolling older scanners cannot
// make a legacy Dolby Vision probe look current merely by retaining a non-NULL
// probe timestamp.
func (v *VideoTrack) UnmarshalJSON(data []byte) error {
	type videoTrackAlias VideoTrack
	var decoded videoTrackAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	current := raw["dv_config_present"] != nil && raw["dv_bl_compat_id_present"] != nil
	*v = VideoTrack(decoded)
	v.DVProvenanceCurrent = &current
	v.HDR10PlusUnchecked = raw["hdr10_plus"] == nil
	return nil
}

// MarshalJSON writes hdr10_plus, false included, for a track whose HDR10+
// status was checked, and leaves it out of an unchecked one.
func (v VideoTrack) MarshalJSON() ([]byte, error) {
	type videoTrackAlias VideoTrack
	if v.HDR10PlusUnchecked {
		return json.Marshal(videoTrackAlias(v))
	}
	return json.Marshal(struct {
		videoTrackAlias
		HDR10Plus bool `json:"hdr10_plus"`
	}{videoTrackAlias(v), v.HDR10Plus})
}

// AudioTrack represents a probed audio stream stored as JSONB.
type AudioTrack struct {
	// Index is the absolute container stream index recorded by the probe:
	// ffprobe's stream index (video at stream 0, the first audio stream
	// typically at 1, subtitles interleaved between audio streams). It is
	// neither the audio-only ordinal ffmpeg's `0:a:N` expects nor this track's
	// position in an AudioTracks array. Clients must select a track with the
	// playback plan's audio inventory selection_index/track_id, never with this
	// value. Preserved so the selected track maps to the real stream even when
	// the track list order differs from the container order (MULTi releases,
	// virtual sources). -1/0 when unknown (synthesized tracks).
	Index         int      `json:"index,omitempty"`
	Title         string   `json:"title,omitempty"`
	EmbeddedTitle string   `json:"embedded_title,omitempty"`
	Language      string   `json:"language,omitempty" doc:"Track language from the file: BCP 47 tag, ISO 639-1 where one exists (en, pt-BR); und when the file marks it undetermined. Older scans may carry an ISO 639-2 code or the file's raw tag"`
	Languages     []string `json:"languages,omitempty" doc:"All track languages as BCP 47 tags, ISO 639-1 where one exists"`
	Codec         string   `json:"codec,omitempty"`
	Profile       string   `json:"profile,omitempty"`
	Layout        string   `json:"layout,omitempty"`
	Channels      int      `json:"channels,omitempty"`
	Bitrate       int      `json:"bitrate,omitempty"`
	SampleRate    int      `json:"sample_rate,omitempty"`
	BitDepth      int      `json:"bit_depth,omitempty"`
	Default       bool     `json:"default"`
}

// SubtitleTrack represents an embedded subtitle track stored as JSONB.
type SubtitleTrack struct {
	ContainerTrackID string `json:"container_track_id,omitempty"`
	Index            int    `json:"index"`
	Language         string `json:"language"`
	Codec            string `json:"codec"`
	Title            string `json:"title,omitempty"`
	EmbeddedTitle    string `json:"embedded_title,omitempty"`
	Resolution       string `json:"resolution,omitempty"`
	Forced           bool   `json:"forced"`
	Default          bool   `json:"default"`
	HearingImpaired  bool   `json:"hearing_impaired"`
	External         bool   `json:"external"`
	FileName         string `json:"file_name,omitempty"`
	// IndexOnly marks an embedded track FFmpeg can't demux from the source,
	// such as a Matroska S_TEXT/WEBVTT track, which the server reads through
	// the container's index instead (embeddedsub). No route that demuxes the
	// source with FFmpeg, on the server or in a client, can carry it.
	IndexOnly bool `json:"index_only,omitempty"`
}

// ExternalSubtitle represents a sidecar subtitle file stored as JSONB.
type ExternalSubtitle struct {
	Path            string `json:"path"`
	Language        string `json:"language"`
	Format          string `json:"format"` // srt, vtt, ass, ssa, sub
	Title           string `json:"title,omitempty"`
	EmbeddedTitle   string `json:"embedded_title,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	Forced          bool   `json:"forced"`
	Default         bool   `json:"default"`
	HearingImpaired bool   `json:"hearing_impaired"`
}

// PersonKind identifies the role type a person has on a media item.
type PersonKind int

const (
	PersonKindActor     PersonKind = 1
	PersonKindDirector  PersonKind = 2
	PersonKindWriter    PersonKind = 3
	PersonKindProducer  PersonKind = 4
	PersonKindGuestStar PersonKind = 5
	PersonKindComposer  PersonKind = 6
	PersonKindAuthor    PersonKind = 7
	PersonKindNarrator  PersonKind = 8
	// PersonKindCreator credits a series' creators, who are not its directors.
	PersonKindCreator PersonKind = 9
)

// String returns the Jellyfin-compatible type string for this PersonKind.
func (k PersonKind) String() string {
	switch k {
	case PersonKindActor:
		return "Actor"
	case PersonKindDirector:
		return "Director"
	case PersonKindWriter:
		return "Writer"
	case PersonKindProducer:
		return "Producer"
	case PersonKindGuestStar:
		return "GuestStar"
	case PersonKindComposer:
		return "Composer"
	case PersonKindAuthor:
		return "Author"
	case PersonKindNarrator:
		return "Narrator"
	case PersonKindCreator:
		return "Creator"
	default:
		return "Unknown"
	}
}

// PersonKindFromJob maps a crew job title to a PersonKind.
func PersonKindFromJob(job string) PersonKind {
	switch strings.ToLower(strings.TrimSpace(job)) {
	case "director":
		return PersonKindDirector
	case "creator":
		return PersonKindCreator
	case "writer", "screenplay", "story", "novel":
		return PersonKindWriter
	case "composer", "original music composer", "music":
		return PersonKindComposer
	default:
		return PersonKindProducer
	}
}

// Person represents a deduplicated person entity.
type Person struct {
	ID              int64
	Name            string
	SortName        string
	Bio             string
	BirthDate       *time.Time
	DeathDate       *time.Time
	Birthplace      string
	Homepage        string
	PhotoPath       string
	PhotoSourcePath string
	PhotoThumbhash  string
	TmdbID          string
	ImdbID          string
	TvdbID          string
	PlexGUID        string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// MetadataRefreshAttemptedAt is when a provider lookup was last attempted
	// for this person, whether or not it returned anything. nil means never
	// attempted. Only the whole-row people loaders in catalog populate it;
	// credit listings (ItemPerson) leave it nil.
	MetadataRefreshAttemptedAt *time.Time
}

// ItemPerson represents a person's credit on a specific media item.
type ItemPerson struct {
	Person
	Kind      PersonKind
	Character string
	SortOrder int
	// LocalPhotoSource is a photo the item's local metadata (an NFO actor
	// <thumb>) names for the person: a file:// sidecar or an http(s) URL. It
	// travels through the metadata pipeline only; the person's photo takes it
	// when the person has no cached photo (see metadata.applyLocalPersonPhotos).
	LocalPhotoSource string
}

// AudiobookSeriesMembership captures a book's membership in an audiobook
// series and its optional sequence number within that series.
type AudiobookSeriesMembership struct {
	Name  string
	Index *float64
	// Key is LOWER(series_name) as the database computes it: the series
	// identity the ABS surface groups, filters, and looks up by.
	Key string
}

// MediaItem represents a row in the media_items table.
// AdvisoryColumns normalizes an advisory age and its source for storage.
//
// The pair is all-or-nothing: an age Silo cannot attribute is an anonymous
// number shown to a parent, and a source with no age says nothing. Whenever
// either half is missing, both columns store NULL, so "no advisory" has one
// spelling in the database rather than a NULL, an empty string, and a zero.
//
// Only movies and series store an advisory (see AdvisoryAgeApplies), so a
// profile's advisory-age limit can never hide an item of any other type, such
// as a title in the beta book libraries.
//
// Every write path funnels through here, including the COPY-based bulk import,
// which cannot lean on a NULLIF in SQL, and the catalog-bundle imports, which
// may carry an advisory from any source.
func AdvisoryColumns(itemType string, age *int, source string) (*int, *string) {
	if !AdvisoryAgeApplies(itemType) || age == nil || *age <= 0 || source == "" {
		return nil, nil
	}
	return age, &source
}

// AdvisoryAgeApplies reports whether an item of itemType may carry an advisory
// age. Only movies and series do: those are the types advisory services rate
// and the only ones the profile advisory-age limit is meant for.
func AdvisoryAgeApplies(itemType string) bool {
	return itemType == advisoryItemTypeMovie || itemType == advisoryItemTypeSeries
}

// The media_items.type values that may carry an advisory age.
const (
	advisoryItemTypeMovie  = "movie"
	advisoryItemTypeSeries = "series"
)

type MediaItem struct {
	ContentID               string // Sonyflake ID (PK)
	Type                    string // movie, series
	Title                   string
	SortTitle               string
	DefaultMetadataLanguage string
	OriginalTitle           string
	Year                    int
	Genres                  []string
	ContentRating           string // PG-13, TV-MA
	// AdvisoryAge is a recommended minimum viewer age from an advisory service
	// (Common Sense Media), distinct from the certification in ContentRating.
	// It never feeds ContentRating or content_rating_age; a profile's
	// separate advisory-age limit (access.MaturityLimits.MaxAdvisoryAge)
	// compares against it, and a nil age never hides a title from that limit.
	// Nil means no advisory; the column is nullable and a stored age is always
	// positive, since providers spell "unknown" as zero.
	AdvisoryAge *int
	// AdvisorySource attributes AdvisoryAge so the UI can name who recommended
	// it. Empty when AdvisoryAge is nil.
	AdvisorySource string
	Runtime        int // minutes
	// AudiobookDurationSeconds is an exact transient duration overlay loaded
	// from active audiobook file stats for protocol adapters that use seconds.
	AudiobookDurationSeconds     int
	Overview                     string
	Tagline                      string
	RatingIMDB                   *float64
	RatingTMDB                   *float64
	RatingRTCritic               *int
	RatingRTAudience             *int
	ImdbID                       string
	TmdbID                       string
	TvdbID                       string
	PosterPath                   string // S3 path
	PosterSourcePath             string // provider-origin path kept when caching rewrites PosterPath; feeds outbound embeds
	PosterThumbhash              string
	BackdropPath                 string
	BackdropSourcePath           string
	BackdropThumbhash            string
	LogoPath                     string
	LogoSourcePath               string
	MetadataS3Path               string
	MetadataEtag                 string
	SeasonCount                  *int // series only
	MangaChapterCount            *int // manga series only: loose manga_chapters rows without a volume token
	MangaVolumeCount             *int // manga series only: distinct non-empty volume tokens in manga_chapters
	Studios                      []string
	Networks                     []string
	Countries                    []string
	Keywords                     []string
	OriginalLanguage             string
	ReleaseDate                  *string // ISO date, nullable
	DigitalReleaseDate           *string // ISO date (movies only): earliest digital release in any country, nullable
	PhysicalReleaseDate          *string // ISO date (movies only): earliest physical (disc) release in any country, nullable
	FirstAirDate                 *string // ISO date (series only), nullable
	LastAirDate                  *string // ISO date (series only), nullable
	AirTime                      *string // Series broadcast time (e.g. "20:00"), nullable
	AirTimezone                  *string // Series broadcast timezone (IANA name, e.g. "America/New_York"), nullable
	ShowStatus                   string  // Series lifecycle: "returning", "ended", "canceled", "in_production", "upcoming", or "" if unknown (series; manga uses its own domain, e.g. "Ongoing")
	People                       []ItemPerson
	AudiobookSeries              []AudiobookSeriesMembership
	MatchedAt                    *time.Time
	EpisodeMetadataIncomplete    bool
	EpisodeMetadataLastCheckedAt *time.Time
	LastRefreshed                *time.Time `json:"last_refreshed,omitempty"`
	RefreshFailures              int        `json:"refresh_failures"`
	LockedFields                 []int      `json:"locked_fields"`
	Status                       string     `json:"status"` // pending, matched, unmatched
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
	AddedAt                      *time.Time // populated by browse queries (MIN(mil.first_seen_at))
	// PlayContentID is transient presentation metadata populated by resolvers
	// whose displayed item differs from the leaf item that should play.
	PlayContentID string
	// MachineTranslatedFields is transient presentation metadata set by
	// localization: the fields ("overview", "tagline") whose localized text
	// came from AI translation rather than a provider or a person.
	MachineTranslatedFields []string `json:"-"`
	// PosterIsEpisodeStill and BackdropIsEpisodeStill are transient provenance
	// set by the query that picks an episode card's artwork: true when the
	// path is the episode's own still, false for season or series artwork, nil
	// when no such query loaded the item. They travel with the paths so a
	// cached item never pairs old artwork with newer provenance. json:"-"
	// because some frozen v1 responses serialize MediaItem directly.
	PosterIsEpisodeStill   *bool `json:"-"`
	BackdropIsEpisodeStill *bool `json:"-"`
	// ParentArtwork is transient presentation metadata on an episode card:
	// its season's and series' own artwork, as raw paths before sizing or
	// signing, set by the query that loads the card. nil when no such query
	// loaded the item. json:"-" for the same frozen v1 responses.
	ParentArtwork *EpisodeParentArtwork `json:"-"`
	// Banner, thumb (landscape), clear art and disc art follow the poster,
	// backdrop and logo slots. Clear art and disc art are cut-outs and, like
	// the logo, carry no thumbhash. json:"-" for the same frozen v1 responses.
	BannerPath         string `json:"-"`
	BannerSourcePath   string `json:"-"`
	BannerThumbhash    string `json:"-"`
	ThumbPath          string `json:"-"`
	ThumbSourcePath    string `json:"-"`
	ThumbThumbhash     string `json:"-"`
	ClearArtPath       string `json:"-"`
	ClearArtSourcePath string `json:"-"`
	DiscPath           string `json:"-"`
	DiscSourcePath     string `json:"-"`
}

// EpisodeParentArtwork is an episode's season poster and its series' poster
// and backdrop, so an episode card can draw a hero from series artwork rather
// than the episode still. Section cards also carry the season backdrop, which
// v2 Continue Watching and Next Up cards show before the series backdrop.
type EpisodeParentArtwork struct {
	SeriesPosterPath        string
	SeriesPosterThumbhash   string
	SeriesBackdropPath      string
	SeriesBackdropThumbhash string
	SeasonPosterPath        string
	SeasonPosterThumbhash   string
	SeasonBackdropPath      string
	SeasonBackdropThumbhash string
}

// ItemBackdrop is one backdrop after an item's primary, a media_item_backdrops
// row. Ordinal 1 is the second backdrop the item shows.
type ItemBackdrop struct {
	ID         int64
	Ordinal    int
	Path       string
	SourcePath string
	Thumbhash  string
}

// MediaItemAlias is a provider-confirmed searchable title for a media item.
type MediaItemAlias struct {
	ContentID string
	Title     string
	Language  string
	Kind      string
	Provider  string
}

// Season represents a row in the seasons table.
type Season struct {
	ContentID               string
	SeriesID                string
	SeasonNumber            int
	Title                   string
	DefaultMetadataLanguage string
	Overview                string
	AirDate                 *time.Time
	PosterPath              string
	PosterSourcePath        string
	PosterThumbhash         string
	MetadataS3Path          string
	MetadataEtag            string
	MetadataSource          string
	CreatedAt               time.Time
	UpdatedAt               time.Time
	// MachineTranslatedFields is transient; see MediaItem.
	MachineTranslatedFields []string `json:"-"`
	// BackdropPath is the season's landscape art, from a metadata provider or
	// an admin pick. Like the poster, the series' Images lock keeps a pick.
	BackdropPath       string
	BackdropSourcePath string
	BackdropThumbhash  string
	// WriteBackdrop is transient: a season upsert writes the three backdrop
	// columns only when it is set, replacing or clearing the stored backdrop.
	// Otherwise the stored backdrop stays, whatever the backdrop fields hold.
	WriteBackdrop bool `json:"-"`
	// BackdropFillOnly, with WriteBackdrop, writes only while the stored
	// backdrop is still empty when the upsert holds the row.
	BackdropFillOnly bool `json:"-"`
	// LockedFields lists the fields a refresh keeps; see catalog.childLockedColumn.
	LockedFields []int
	// No metadata provider writes these; admin edits and seed imports do.
	OriginalTitle string
	Tagline       string
	ImdbID        string
	TmdbID        string
	TvdbID        string
}

// Episode represents a row in the episodes table.
type Episode struct {
	ContentID               string // episode Sonyflake ID (PK)
	SeriesID                string
	SeasonID                string // FK to seasons.content_id
	SeasonNumber            int
	EpisodeNumber           int
	Title                   string
	DefaultMetadataLanguage string
	Overview                string
	AirDate                 *time.Time
	Runtime                 int // minutes
	RatingIMDB              *float64
	RatingTMDB              *float64
	ImdbID                  string
	TmdbID                  string
	TvdbID                  string
	StillPath               string
	StillSourcePath         string
	StillThumbhash          string
	MetadataS3Path          string
	MetadataEtag            string
	MetadataSource          string
	CreatedAt               time.Time
	UpdatedAt               time.Time
	// MachineTranslatedFields is transient; see MediaItem.
	MachineTranslatedFields []string `json:"-"`
	// PendingTranslationLanguage is transient presentation metadata set by
	// batch localization: the viewer's language this episode's description is
	// missing, so an episode list can start on-view translation.
	PendingTranslationLanguage string `json:"-"`
	// LockedFields lists the fields a refresh keeps; see catalog.childLockedColumn.
	LockedFields []int
	// No metadata provider writes these; admin edits and seed imports do.
	SortTitle     string
	OriginalTitle string
	Tagline       string
}

// MediaItemRoot represents a row in the media_item_roots table.
// It records which content_id owns a given canonical root path within a library folder.
type MediaItemRoot struct {
	MediaFolderID     int
	CanonicalRootPath string
	ContentID         string
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
}

// MediaItemGroup represents a row in the media_item_groups table.
type MediaItemGroup struct {
	MediaFolderID   int
	GroupKeyVersion int
	ContentGroupKey string
	ContentID       string
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
}

// MediaItemLibrary represents a row in the media_item_libraries junction table.
type MediaItemLibrary struct {
	ContentID     string
	MediaFolderID int
	FirstSeenAt   time.Time
}

// EpisodeLibrary represents a row in the episode_libraries junction table.
type EpisodeLibrary struct {
	EpisodeID     string
	MediaFolderID int
	FirstSeenAt   time.Time
}

// Localization field provenance values. Precedence when writing:
// manual beats provider beats ai — a provider refresh may overwrite an AI
// translation but never a manual edit, and AI never overwrites either, not
// even when the admin forces a re-translation.
const (
	LocalizationSourceProvider = "provider"
	LocalizationSourceAI       = "ai"
	LocalizationSourceManual   = "manual"
)

type MediaItemLocalization struct {
	ContentID          string
	Language           string
	Title              string
	SortTitle          string
	Overview           string
	Tagline            string
	PosterPath         string
	PosterSourcePath   string
	PosterThumbhash    string
	BackdropPath       string
	BackdropSourcePath string
	BackdropThumbhash  string
	LogoPath           string
	LogoSourcePath     string
	OverviewSource     string // provider | ai | manual
	TaglineSource      string // provider | ai | manual
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type SeasonLocalization struct {
	SeasonContentID  string
	Language         string
	Title            string
	Overview         string
	PosterPath       string
	PosterSourcePath string
	PosterThumbhash  string
	OverviewSource   string // provider | ai | manual
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type EpisodeLocalization struct {
	EpisodeContentID string
	Language         string
	Title            string
	Overview         string
	OverviewSource   string // provider | ai | manual
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// PersistedVideoCopyVerdict returns the H.264 multi-PPS verdict recorded on the
// media_files row and whether it still describes the file as it stands.
func (f *MediaFile) PersistedVideoCopyVerdict() (bool, bool) {
	if f == nil || f.MultiplePPS == nil || f.MultiplePPSScanSize == nil {
		return false, false
	}
	if *f.MultiplePPSScanSize != f.FileSize {
		return false, false
	}
	if f.MultiplePPSScanMtime == nil || f.FileModifiedAt == nil {
		if f.MultiplePPSScanMtime != nil || f.FileModifiedAt != nil {
			return false, false
		}
		return *f.MultiplePPS, true
	}
	if !NormalizeFileModifiedAt(*f.MultiplePPSScanMtime).Equal(NormalizeFileModifiedAt(*f.FileModifiedAt)) {
		return false, false
	}
	return *f.MultiplePPS, true
}

// NormalizeFileModifiedAt puts a filesystem mtime in the one shape every
// comparison uses. Postgres stores microseconds and local filesystems report
// nanoseconds, so a round trip through the database is only equal to the value
// that was written after truncation.
func NormalizeFileModifiedAt(ts time.Time) time.Time {
	return ts.UTC().Truncate(time.Microsecond)
}

// VideoCopySafetyUnknown reports whether this file is an H.264 video whose
// multi-PPS copy-safety verdict is not stamped on the in-memory track. Only
// H.264 can carry the conflicting in-band parameter sets that make a video
// stream-copy unsafe, so every other codec is trivially known-safe.
//
// This is the single definition of "the verdict is still open", shared by the
// scanner that resolves it, the catalog surfaces that trigger the resolution,
// and playback.
func (f *MediaFile) VideoCopySafetyUnknown() bool {
	if f == nil || len(f.VideoTracks) == 0 {
		return false
	}
	if f.VideoTracks[0].MultiplePPS != nil {
		return false
	}
	codec := strings.ToLower(strings.TrimSpace(f.VideoTracks[0].Codec))
	if codec == "" {
		codec = strings.ToLower(strings.TrimSpace(f.CodecVideo))
	}
	return codec == "h264" || codec == "avc" || codec == "avc1"
}

// VirtualFilePersistArgs carries the full persistence payload for a virtual
// probe result. The SQL update is CAS-fenced on the caller's snapshot to
// prevent stale or out-of-order probes from overwriting newer evidence.
// Defined here (not in the handler packages) so both the native and
// Jellyfin-compat persistence paths share one contract without an import
// cycle.
type VirtualFilePersistArgs struct {
	FileID           int
	ExpectedFilePath string // CAS: what file_path should be in the DB right now
	VideoTracks      []byte
	AudioTracks      []byte
	SubtitleTracks   []byte
	Resolution       string
	CodecVideo       string
	CodecAudio       string
	Container        string
	HDR              bool
	Bitrate          int
	Duration         int
	StampProbe       bool
	// CAS fence fields — caller snapshots these from the catalog row
	// *before* resolution/probing so a stale background result cannot
	// overwrite evidence committed since the snapshot was taken.
	UpdatedAt      time.Time
	ProbeUpdatedAt *time.Time
	OwnerID        int
	LibraryID      int
	// AdoptPath: if non-empty, atomically adopt this file_path (unless
	// probe_source is 'virtual_collection'). Empty string retains the
	// current path.
	AdoptPath string
	// RequireAdopt makes identity adoption a condition of success: when
	// AdoptPath is non-empty the saver must confirm the row's file_path became
	// AdoptPath, and must report a non-nil error when a uniqueness conflict,
	// the probe_source guard, or a live failed_at verdict prevented adoption.
	// The write is atomic with that fence: a refused adoption writes no track
	// inventory or probe stamp. Metadata-only evidence writers leave this false
	// so a skipped adoption is not an error and the metadata still lands.
	RequireAdopt bool
	// AllowFailedVerdict is the explicit-retry policy. When true, an active
	// failed_at stamp on the validated identity does not block adoption; the
	// caller has deliberately asked to retry a known-bad candidate. The
	// automatic path leaves it false so a failure committed after the caller's
	// last verdict read is still fenced out at write time.
	AllowFailedVerdict bool
	// SameReleaseRematch marks a required adoption the resolver certified as the
	// same release under a new provider result id (a renumber). When the fence
	// refuses such an adoption — the new id's path is already owned by a sibling
	// row of that release, or carries a live failed verdict — the refusal is not
	// stale-pin evidence: the row's release is proven, and its pin simply cannot
	// move onto a path the catalog already assigns elsewhere. The saver logs the
	// concrete cause but does not count the refusal, so a healthy row cannot be
	// marched to a terminal failed verdict by a duplicate that owns the id.
	SameReleaseRematch bool
	// ExpectedProvider* is the adopting row's current durable identity,
	// snapshotted with the CAS fields. When AdoptPath is set, the saver compares
	// it to the incoming Provider* identity to decide whether the adopted
	// candidate is the same release (preserve omitted transport fields) or a
	// different release (replace the whole transport set). It must be the row's
	// identity, never the candidate's: comparing the candidate to itself would
	// always report same-release. An identity tier present on only one side is
	// not a same-release proof, so a required adoption with an unprovable match
	// replaces rather than preserves.
	ExpectedProviderVideoHash   string
	ExpectedProviderGUID        string
	ExpectedProviderReleaseName string
	ExpectedProviderReleaseSize int64
	// ClearProbe invalidates the row's probe evidence in the same CAS-fenced
	// write: probe_source and probe_updated_at are set to NULL (collection-owned
	// rows keep theirs). A caller uses it when it adopts bytes whose inventory
	// the stored probe does not describe, so the next start re-probes and
	// converges the row onto the real file instead of trusting stale evidence.
	// It is independent of StampProbe: a write either stamps or clears, never
	// both.
	ClearProbe bool
	// ResolvedURL is the provider stream URL a successful resolution produced
	// for this row. When non-empty it (re)writes resolved_url and its paired
	// expiry; when empty the stored values are preserved, so a metadata-only
	// write cannot erase the last resolved URL. ResolvedURLExpiresAt is the
	// URL's parsed expiry (nil when unparseable).
	ResolvedURL          string
	ResolvedURLExpiresAt *time.Time
	// Provider* is the candidate's durable identity, in the same tier order
	// as the dedup key. A non-empty value overwrites the stored one; an empty
	// value preserves it, so a metadata-only write cannot erase identity.
	ProviderVideoHash   string
	ProviderGUID        string
	ProviderReleaseName string
	ProviderReleaseSize int64
	// ProviderRequestHeaders is the request header set the resolved URL needs.
	// A non-nil value (re)writes provider_request_headers; a nil value
	// preserves the stored one, so a metadata-only write cannot erase the
	// headers a resolved URL depends on.
	ProviderRequestHeaders map[string]string
	// ReconcileCollectionVariant is the recorded verdict that a collection-owned
	// variant row's pinned release has genuinely vanished from the provider's
	// fresh listing and the row may be re-pointed at the profile's live
	// candidate. Collection-owned paths are otherwise immutable (the collection
	// sync owns them), so this must be set only by a caller that listed the
	// provider, matched the pinned result id AND durable identity against the
	// fresh set, and found neither. With it set, the path-adoption guard admits
	// the collection row so the adoption can land atomically with the probed
	// evidence; without it a collection path is never rewritten.
	ReconcileCollectionVariant bool
	// RefusalReason names the concrete cause the caller expects a required
	// adoption to be refused for, so the saver's refusal log can distinguish a
	// sibling path owner from a live failed verdict, or a collection row from a
	// stale CAS snapshot, instead of emitting one lumped bucket. It is only
	// consulted on refusal: a write that succeeds ignores it. An empty value
	// falls back to a neutral reason, so callers that do not classify stay
	// honest rather than guessing.
	RefusalReason string
}
