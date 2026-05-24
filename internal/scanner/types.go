package scanner

// ScanResult contains the outcome of scanning a media folder.
type ScanResult struct {
	New                int
	Updated            int
	Unchanged          int
	Missing            int
	FilesDeleted       int
	MembershipsRemoved int
	ItemsDeleted       int
	Errors             int
	EmptyRootGuarded   bool
	RootObservations   []RootObservation
}

// FileHints contains the OSHash gathered during scanning.
type FileHints struct {
	FileHash string // OSHash from xattr or computed
}

// ProbeData contains media file technical information from ffprobe.
type ProbeData struct {
	CodecVideo     string
	CodecAudio     string
	Resolution     string // 1080p, 2160p, etc.
	AudioChannels  int
	HDR            bool
	Container      string
	Duration       int // seconds
	Bitrate        int // kbps
	VideoTracks    []VideoTrackInfo
	AudioTracks    []AudioTrackInfo
	SubtitleTracks []SubtitleTrackInfo
	Chapters       []ChapterInfo
}

// VideoTrackInfo describes a probed video track.
type VideoTrackInfo struct {
	Title           string
	Codec           string
	DolbyVision     string
	Profile         string
	Level           int
	Width           int
	Height          int
	AspectRatio     string
	Interlaced      bool
	FrameRate       string
	Bitrate         int
	VideoRange      string
	ColorPrimaries  string
	ColorSpace      string
	ColorTransfer   string
	BitDepth        int
	PixelFormat     string
	ReferenceFrames int
}

// AudioTrackInfo describes a probed audio track.
type AudioTrackInfo struct {
	Title         string
	EmbeddedTitle string
	Language      string
	Codec         string
	Layout        string
	Channels      int
	Bitrate       int
	SampleRate    int
	BitDepth      int
	Default       bool
}

// SubtitleTrackInfo describes an embedded subtitle track from probing.
type SubtitleTrackInfo struct {
	Index           int
	Language        string
	Codec           string
	Title           string
	EmbeddedTitle   string
	Resolution      string
	Forced          bool
	Default         bool
	HearingImpaired bool
}

// ExternalSubtitleInfo describes a discovered sidecar subtitle file.
type ExternalSubtitleInfo struct {
	Path     string
	Language string
	Format   string // srt, vtt, ass, ssa, sub
	Title    string
	Forced   bool
}

// ChapterInfo describes an embedded chapter extracted from ffprobe.
type ChapterInfo struct {
	Index        int
	Title        string
	StartSeconds float64
	EndSeconds   float64
	Source       string
}

// IntroCreditsMarkers contains intro/credits timing from S3 markers.
type IntroCreditsMarkers struct {
	IntroStart   *float64
	IntroEnd     *float64
	CreditsStart *float64
	CreditsEnd   *float64
}

// MarkerUpdate is the narrow marker-only update payload shared by scanner and analyzers.
type MarkerUpdate struct {
	IntroStart        *float64
	IntroEnd          *float64
	CreditsStart      *float64
	CreditsEnd        *float64
	RecapStart        *float64
	RecapEnd          *float64
	PreviewStart      *float64
	PreviewEnd        *float64
	MarkersSource     string
	MarkersProvider   *string
	MarkersConfidence *float64
	MarkersAlgorithm  string
}

// HasAnySegment reports whether the update would write at least one segment.
// An update with no segment bounds set is a no-op and skipped by UpsertMarkers.
func (u MarkerUpdate) HasAnySegment() bool {
	return u.IntroStart != nil || u.IntroEnd != nil ||
		u.CreditsStart != nil || u.CreditsEnd != nil ||
		u.RecapStart != nil || u.RecapEnd != nil ||
		u.PreviewStart != nil || u.PreviewEnd != nil
}
