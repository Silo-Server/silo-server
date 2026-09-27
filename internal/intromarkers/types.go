package intromarkers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// AlgorithmVersion keys the fingerprint cache; changing it discards every
// stored fingerprint. Bump AnalysisBehaviorVersion instead to re-run season
// comparisons over cached fingerprints.
//
// The algorithm identifiers are persisted with each marker. A new version
// needs a rank in markers.scannerAlgorithmPriority above the one it replaces,
// or re-analysis cannot overwrite markers the old version wrote.
const (
	AlgorithmVersion             = 1
	AnalysisBehaviorVersion      = 4
	ChapterAlgorithm             = "chapter:v1"
	ChapterSilenceAlgorithm      = "chapter:silence:v2"
	EpisodeVersionCopyAlgorithm  = "episode-version-copy:v1"
	ChromaprintAlgorithm         = "chromaprint:v4"
	ChromaprintDialogueAlgorithm = "chromaprint:dialogue:v4" //nolint:misspell // Persisted algorithm identifier.
	ChromaprintFormat            = "chromaprint:raw:uint32le"
	DefaultPointHopSeconds       = 0.123

	// Credits algorithm identifiers. Credits season comparisons are keyed by
	// CreditsAnalysisConfigHash; bump CreditsBehaviorVersion to re-run them
	// over cached credits fingerprints.
	CreditsBehaviorVersion      = 2
	CreditsChapterAlgorithm     = "credits-chapter:v1"
	CreditsVersionCopyAlgorithm = "credits-version-copy:v1"
	CreditsAudioAlgorithm       = "credits-audio:v1"
	CreditsAudioVideoAlgorithm  = "credits-audio:video:v1"
	CreditsVideoAlgorithm       = "credits-video:v1"

	// chromaprintDialogueAlgorithmPrefix matches every version of the
	// subtitle-refined Chromaprint identifier.
	chromaprintDialogueAlgorithmPrefix = "chromaprint:dialogue:" //nolint:misspell // Persisted algorithm identifier.

	// legacyChapterSilenceAlgorithm extended chapter ends by up to 30 seconds.
	// The silence backfill revisits its markers under the current limit.
	legacyChapterSilenceAlgorithm = "chapter:silence:v1"
)

type Config struct {
	FFmpegPath                                string
	MaxParallelFFmpeg                         int
	AnalysisPercent                           int
	AnalysisLengthLimitMinutes                int
	MinimumIntroDurationSeconds               int
	MaximumIntroDurationSeconds               int
	SilenceRefinementEnabled                  bool
	SilenceWindowBeforeSeconds                float64
	SilenceWindowAfterSeconds                 float64
	SilenceMinimumDurationSeconds             float64
	SilenceNoiseThresholdDB                   *int
	SilenceMinimumExtensionSeconds            float64
	SilenceMaximumExtensionSeconds            float64
	SilenceBackfillLimit                      int
	SilenceBackfillMaxDuration                time.Duration
	DialogueRefinementEnabled                 bool
	DialogueRefinementWindowSeconds           float64
	DialogueRefinementMaxShiftSeconds         float64
	DialogueRefinementMinimumRemainingSeconds float64
	// HWAccel and HWDevice are the playback.hw_accel and playback.hw_device
	// settings. Credits tail passes decode keyframes on the hardware they
	// resolve to and fall back to software (see hwdecode.go). They shape no
	// artifact or hash.
	HWAccel  string
	HWDevice string
}

// Intro duration bounds for a Chromaprint match. Twelve seconds keeps most
// short title cards: in replay against authored chapters, lowering the bound
// further mostly added matches in the wrong place. Three minutes covers long
// drama openings, inside TheIntroDB's limit.
const (
	defaultMinimumIntroDurationSeconds = 12
	defaultMaximumIntroDurationSeconds = 180
)

// The fingerprint cache key once hashed the intro duration bounds, which do not
// shape a fingerprint. They are hashed as these fixed values so the bounds can
// change without discarding every cached fingerprint.
const (
	fingerprintKeyMinimumIntroSeconds = 15
	fingerprintKeyMaximumIntroSeconds = 120
)

// defaultSilenceMaximumExtensionSeconds bounds how far a silence may move an
// authored intro chapter's end. Short extensions catch music that rings past
// the chapter mark; against Chromaprint's audio match, extensions of five
// seconds or more mostly overshot the chapter end into the episode.
const defaultSilenceMaximumExtensionSeconds = 5

// DefaultDetectionWorkers is how many seasons intro detection analyzes at
// once, and so how many ffmpeg processes it runs, unless an administrator
// raises markers.detection_workers. One keeps a shared server's storage and
// CPU free for playback.
const DefaultDetectionWorkers = 1

func DefaultConfig(ffmpegPath string) Config {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	return Config{
		FFmpegPath:                                ffmpegPath,
		MaxParallelFFmpeg:                         DefaultDetectionWorkers,
		AnalysisPercent:                           25,
		AnalysisLengthLimitMinutes:                10,
		MinimumIntroDurationSeconds:               defaultMinimumIntroDurationSeconds,
		MaximumIntroDurationSeconds:               defaultMaximumIntroDurationSeconds,
		SilenceRefinementEnabled:                  true,
		SilenceWindowBeforeSeconds:                3,
		SilenceWindowAfterSeconds:                 30,
		SilenceMinimumDurationSeconds:             0.33,
		SilenceNoiseThresholdDB:                   intPtr(-50),
		SilenceMinimumExtensionSeconds:            0.5,
		SilenceMaximumExtensionSeconds:            defaultSilenceMaximumExtensionSeconds,
		SilenceBackfillLimit:                      2000,
		SilenceBackfillMaxDuration:                45 * time.Minute,
		DialogueRefinementEnabled:                 true,
		DialogueRefinementWindowSeconds:           15,
		DialogueRefinementMaxShiftSeconds:         20,
		DialogueRefinementMinimumRemainingSeconds: defaultMinimumIntroDurationSeconds,
	}
}

func (c Config) normalized() Config {
	if strings.TrimSpace(c.FFmpegPath) == "" {
		c.FFmpegPath = "ffmpeg"
	}
	if c.MaxParallelFFmpeg <= 0 {
		c.MaxParallelFFmpeg = 1
	}
	if c.AnalysisPercent <= 0 {
		c.AnalysisPercent = 25
	}
	if c.AnalysisLengthLimitMinutes <= 0 {
		c.AnalysisLengthLimitMinutes = 10
	}
	if c.MinimumIntroDurationSeconds <= 0 {
		c.MinimumIntroDurationSeconds = defaultMinimumIntroDurationSeconds
	}
	if c.MaximumIntroDurationSeconds <= 0 {
		c.MaximumIntroDurationSeconds = defaultMaximumIntroDurationSeconds
	}
	if c.SilenceWindowBeforeSeconds <= 0 {
		c.SilenceWindowBeforeSeconds = 3
	}
	if c.SilenceWindowAfterSeconds <= 0 {
		c.SilenceWindowAfterSeconds = 30
	}
	if c.SilenceMinimumDurationSeconds <= 0 {
		c.SilenceMinimumDurationSeconds = 0.33
	}
	if c.SilenceNoiseThresholdDB == nil || *c.SilenceNoiseThresholdDB > 0 {
		c.SilenceNoiseThresholdDB = intPtr(-50)
	}
	if c.SilenceMinimumExtensionSeconds <= 0 {
		c.SilenceMinimumExtensionSeconds = 0.5
	}
	if c.SilenceMaximumExtensionSeconds <= 0 {
		c.SilenceMaximumExtensionSeconds = defaultSilenceMaximumExtensionSeconds
	}
	if c.SilenceBackfillLimit <= 0 {
		c.SilenceBackfillLimit = 2000
	}
	if c.SilenceBackfillMaxDuration <= 0 {
		c.SilenceBackfillMaxDuration = 45 * time.Minute
	}
	if c.DialogueRefinementWindowSeconds <= 0 {
		c.DialogueRefinementWindowSeconds = 15
	}
	if c.DialogueRefinementMaxShiftSeconds <= 0 {
		c.DialogueRefinementMaxShiftSeconds = 20
	}
	if c.DialogueRefinementMinimumRemainingSeconds <= 0 {
		c.DialogueRefinementMinimumRemainingSeconds = float64(c.MinimumIntroDurationSeconds)
	}
	return c
}

func intPtr(value int) *int {
	return &value
}

// ConfigHash keys the fingerprint cache. Only the analysis window shapes a
// fingerprint.
func (c Config) ConfigHash() string {
	c = c.normalized()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%d",
		c.AnalysisPercent,
		c.AnalysisLengthLimitMinutes,
		fingerprintKeyMinimumIntroSeconds,
		fingerprintKeyMaximumIntroSeconds,
	)))
	return hex.EncodeToString(sum[:])[:16]
}

// AnalysisConfigHash keys season analysis state: the fingerprint key plus
// every setting that changes a season's result without changing its
// fingerprints.
func (c Config) AnalysisConfigHash() string {
	c = c.normalized()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%t:%.3f:%.3f:%.3f:%d:%d",
		c.ConfigHash(),
		AnalysisBehaviorVersion,
		c.DialogueRefinementEnabled,
		c.DialogueRefinementWindowSeconds,
		c.DialogueRefinementMaxShiftSeconds,
		c.DialogueRefinementMinimumRemainingSeconds,
		c.MinimumIntroDurationSeconds,
		c.MaximumIntroDurationSeconds,
	)))
	return hex.EncodeToString(sum[:])[:16]
}

// SilenceConfigHash identifies the settings a chapter silence refinement runs
// with, so a recorded attempt stops matching when any of them change. It does
// not cover the refiner's code: bump ChapterSilenceAlgorithm when a change to
// RefineChapterEnd should re-run files already recorded as no improvement.
func (c Config) SilenceConfigHash() string {
	c = c.normalized()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%.3f:%.3f:%.3f:%d:%.3f:%.3f",
		ChapterSilenceAlgorithm,
		c.SilenceWindowBeforeSeconds,
		c.SilenceWindowAfterSeconds,
		c.SilenceMinimumDurationSeconds,
		*c.SilenceNoiseThresholdDB,
		c.SilenceMinimumExtensionSeconds,
		c.SilenceMaximumExtensionSeconds,
	)))
	return hex.EncodeToString(sum[:])[:16]
}

type Candidate struct {
	ContentID       string
	ExtraID         string
	SeasonNumber    int
	EpisodeNumber   int
	FileModifiedAt  *time.Time
	FileID          int
	EpisodeID       string
	SeasonID        string
	MediaFolderID   int
	FilePath        string
	FileHash        string
	FileSize        int64
	DurationSeconds float64
	// CodecVideo and CodecAudio are the probed codecs of the file's first
	// video and audio streams, empty when it has none.
	CodecVideo string
	CodecAudio string
	// VideoBitDepth is the probed bit depth of the first video stream, zero
	// when unknown. Hardware decoding on VideoToolbox needs it.
	VideoBitDepth          int
	PresentationGroupKey   string
	EditionKey             string
	AudioLanguage          string
	Chapters               []models.MediaChapter
	ChaptersHash           string
	SubtitleTracks         []models.SubtitleTrack
	ExternalSubtitles      []models.ExternalSubtitle
	IntroStart             *float64
	IntroEnd               *float64
	IntroMarkersSource     *string
	IntroMarkersConfidence *float64
	IntroMarkersAlgorithm  *string
	// Credits fields let each marker kind be judged on its own; see
	// Candidate.marker.
	CreditsStart             *float64
	CreditsEnd               *float64
	CreditsMarkersSource     *string
	CreditsMarkersConfidence *float64
	CreditsMarkersAlgorithm  *string
	// PreviewStart is where the file's preview marker starts, which ends
	// any credits it starts inside.
	PreviewStart  *float64
	MarkersSource *string
}

// expectedFile preserves the identity loaded with the candidate so a completed
// analysis cannot write markers onto a replacement file.
func (c Candidate) expectedFile() *models.MediaFile {
	return &models.MediaFile{
		ID:             c.FileID,
		ContentID:      c.ContentID,
		EpisodeID:      c.EpisodeID,
		ExtraID:        c.ExtraID,
		SeasonNumber:   c.SeasonNumber,
		EpisodeNumber:  c.EpisodeNumber,
		FileHash:       c.FileHash,
		FileSize:       c.FileSize,
		FileModifiedAt: c.FileModifiedAt,
		Duration:       int(c.DurationSeconds),
	}
}

func (c Candidate) AnalysisGroupKey() string {
	group := strings.TrimSpace(c.PresentationGroupKey)
	if group == "" {
		group = "default"
	}
	edition := strings.TrimSpace(c.EditionKey)
	if edition == "" {
		edition = "default"
	}
	audio := strings.TrimSpace(c.AudioLanguage)
	if audio == "" {
		audio = "und"
	}
	return strings.Join([]string{group, edition, audio}, "|")
}

type Segment struct {
	Start      float64
	End        float64
	Confidence float64
	Algorithm  string
}

// MarkerPatch is a detected marker of one kind to write onto a file.
type MarkerPatch struct {
	Kind         markerKind
	ExpectedFile *models.MediaFile
	FileID       int
	Start        float64
	End          float64
	Source       string
	Confidence   float64
	Algorithm    string
	DetectedAt   time.Time
}

type Fingerprint struct {
	MediaFileID           int
	FileHash              string
	FileSize              int64
	DurationSeconds       float64
	WindowStartSeconds    float64
	WindowEndSeconds      float64
	AlgorithmVersion      int
	ConfigHash            string
	FingerprintFormat     string
	SampleDurationSeconds float64
	Points                []uint32
}

type SeasonState struct {
	SeasonID         string
	MediaFolderID    int
	AnalysisGroupKey string
	InputSignature   string
	EpisodeCount     int
	FileCount        int
	Status           string
	MarkersWritten   int
	LastError        string
	AnalyzedAt       time.Time
}

const (
	seasonStatusComplete = "complete"
	seasonStatusNotFound = "not_found"
	seasonStatusFailed   = "failed"
	// seasonStatusPartial marks a group analyzed while some fingerprint
	// extractions failed. It is retried after partialSeasonRetryInterval
	// even when its inputs have not changed.
	seasonStatusPartial = "partial"

	partialSeasonRetryInterval = 7 * 24 * time.Hour
)

// settled reports whether a stored analysis still stands for unchanged
// inputs at now.
func (s SeasonState) settled(now time.Time) bool {
	switch s.Status {
	case seasonStatusComplete, seasonStatusNotFound:
		return true
	case seasonStatusPartial:
		return now.Sub(s.AnalyzedAt) < partialSeasonRetryInterval
	default:
		return false
	}
}

const (
	silenceAttemptNoImprovement = "no_improvement"
	silenceAttemptFailed        = "failed"
)

// SilenceRefinementAttempt records a chapter silence refinement that kept the
// chapter boundary, together with the inputs it ran against.
type SilenceRefinementAttempt struct {
	MediaFileID     int
	ConfigHash      string
	FileHash        string
	FileSize        int64
	DurationSeconds float64
	ChaptersHash    string
	IntroStart      float64
	IntroEnd        float64
	Status          string
	RecordedBy      string
	FailureCount    int
	LastError       string
	AttemptedAt     time.Time
	RetryAfter      *time.Time
}

func (a SilenceRefinementAttempt) sameInputs(other SilenceRefinementAttempt) bool {
	return a.MediaFileID == other.MediaFileID &&
		a.ConfigHash == other.ConfigHash &&
		a.FileHash == other.FileHash &&
		a.FileSize == other.FileSize &&
		a.DurationSeconds == other.DurationSeconds &&
		a.ChaptersHash == other.ChaptersHash &&
		a.IntroStart == other.IntroStart &&
		a.IntroEnd == other.IntroEnd
}

type RunSummary struct {
	LibrariesScanned             int      `json:"libraries_scanned"`
	FilesConsidered              int      `json:"files_considered"`
	SeasonGroupsConsidered       int      `json:"season_groups_considered"`
	FingerprintsComputed         int      `json:"fingerprints_computed"`
	FingerprintCacheHits         int      `json:"fingerprint_cache_hits"`
	FingerprintExtractionErrors  int      `json:"fingerprint_extraction_errors"`
	ChapterMarkersWritten        int      `json:"chapter_markers_written"`
	ChromaprintMarkersWritten    int      `json:"chromaprint_markers_written"`
	GroupsNotFound               int      `json:"groups_not_found"`
	GroupsSkipped                int      `json:"groups_skipped"`
	Errors                       []string `json:"errors,omitempty"`
	ChromaprintSupported         bool     `json:"chromaprint_supported"`
	ChromaprintSupportMessage    string   `json:"chromaprint_support_message,omitempty"`
	SilenceRefinementsAttempted  int      `json:"silence_refinements_attempted"`
	SilenceRefinementsApplied    int      `json:"silence_refinements_applied"`
	SilenceRefinementErrors      int      `json:"silence_refinement_errors"`
	EpisodeVersionMarkersCopied  int      `json:"episode_version_markers_copied"`
	SilenceBackfillConsidered    int      `json:"silence_backfill_considered"`
	DialogueRefinementsAttempted int      `json:"dialogue_refinements_attempted"`
	DialogueRefinementsApplied   int      `json:"dialogue_refinements_applied"`
	DialogueRefinementErrors     int      `json:"dialogue_refinement_errors"`
	// Credits counters. Credits season groups and tail fingerprints are
	// counted apart from the intro groups and fingerprints above.
	CreditsSeasonGroupsConsidered int `json:"credits_season_groups_considered"`
	CreditsGroupsNotFound         int `json:"credits_groups_not_found"`
	CreditsGroupsSkipped          int `json:"credits_groups_skipped"`
	CreditsChapterMarkersWritten  int `json:"credits_chapter_markers_written"`
	CreditsVersionMarkersCopied   int `json:"credits_version_markers_copied"`
	CreditsFingerprintsComputed   int `json:"credits_fingerprints_computed"`
	CreditsFingerprintCacheHits   int `json:"credits_fingerprint_cache_hits"`
	CreditsFingerprintErrors      int `json:"credits_fingerprint_errors"`
	CreditsAudioMarkersWritten    int `json:"credits_audio_markers_written"`
	CreditsRejected               int `json:"credits_rejected"`
	// Credits tail pass counters: keyframe statistics and silences of the
	// files whose credits are placed, and the markers video helped place.
	CreditsTailScansComputed        int `json:"credits_tail_scans_computed"`
	CreditsTailCacheHits            int `json:"credits_tail_cache_hits"`
	CreditsTailScanErrors           int `json:"credits_tail_scan_errors"`
	CreditsTailUnusable             int `json:"credits_tail_unusable"`
	CreditsAudioVideoMarkersWritten int `json:"credits_audio_video_markers_written"`
	CreditsVideoMarkersWritten      int `json:"credits_video_markers_written"`
	// Movie counters. Movies get credits only, from chapters and video; the
	// credits tail counters above include their tail passes.
	MoviesConsidered           int `json:"movies_considered"`
	MovieCreditsMarkersWritten int `json:"movie_credits_markers_written"`
	// MovieBudgetExhausted reports that the run stopped starting movies
	// when the movie budget ran out; the rest wait for the next run.
	MovieBudgetExhausted bool `json:"movie_budget_exhausted"`
}
