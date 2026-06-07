package intromarkers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	AlgorithmVersion             = 1
	AnalysisBehaviorVersion      = 2
	ChapterAlgorithm             = "chapter:v1"
	ChapterSilenceAlgorithm      = "chapter:silence:v1"
	EpisodeVersionCopyAlgorithm  = "episode-version-copy:v1"
	ChromaprintAlgorithm         = "chromaprint:v1"
	ChromaprintDialogueAlgorithm = "chromaprint:dialogue:v1"
	ChromaprintFormat            = "chromaprint:raw:uint32le"
	DefaultPointHopSeconds       = 0.123
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
}

func DefaultConfig(ffmpegPath string) Config {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	return Config{
		FFmpegPath:                                ffmpegPath,
		MaxParallelFFmpeg:                         1,
		AnalysisPercent:                           25,
		AnalysisLengthLimitMinutes:                10,
		MinimumIntroDurationSeconds:               15,
		MaximumIntroDurationSeconds:               120,
		SilenceRefinementEnabled:                  true,
		SilenceWindowBeforeSeconds:                3,
		SilenceWindowAfterSeconds:                 30,
		SilenceMinimumDurationSeconds:             0.33,
		SilenceNoiseThresholdDB:                   intPtr(-50),
		SilenceMinimumExtensionSeconds:            0.5,
		SilenceMaximumExtensionSeconds:            30,
		SilenceBackfillLimit:                      2000,
		SilenceBackfillMaxDuration:                45 * time.Minute,
		DialogueRefinementEnabled:                 true,
		DialogueRefinementWindowSeconds:           15,
		DialogueRefinementMaxShiftSeconds:         20,
		DialogueRefinementMinimumRemainingSeconds: 15,
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
		c.MinimumIntroDurationSeconds = 15
	}
	if c.MaximumIntroDurationSeconds <= 0 {
		c.MaximumIntroDurationSeconds = 120
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
		c.SilenceMaximumExtensionSeconds = 30
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

func (c Config) ConfigHash() string {
	c = c.normalized()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%d",
		c.AnalysisPercent,
		c.AnalysisLengthLimitMinutes,
		c.MinimumIntroDurationSeconds,
		c.MaximumIntroDurationSeconds,
	)))
	return hex.EncodeToString(sum[:])[:16]
}

func (c Config) AnalysisConfigHash() string {
	c = c.normalized()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%t:%.3f:%.3f:%.3f",
		c.ConfigHash(),
		AnalysisBehaviorVersion,
		c.DialogueRefinementEnabled,
		c.DialogueRefinementWindowSeconds,
		c.DialogueRefinementMaxShiftSeconds,
		c.DialogueRefinementMinimumRemainingSeconds,
	)))
	return hex.EncodeToString(sum[:])[:16]
}

type Candidate struct {
	FileID                 int
	EpisodeID              string
	SeasonID               string
	MediaFolderID          int
	FilePath               string
	FileHash               string
	FileSize               int64
	DurationSeconds        float64
	PresentationGroupKey   string
	EditionKey             string
	AudioLanguage          string
	Chapters               []models.MediaChapter
	SubtitleTracks         []models.SubtitleTrack
	ExternalSubtitles      []models.ExternalSubtitle
	IntroStart             *float64
	IntroEnd               *float64
	IntroMarkersSource     *string
	IntroMarkersConfidence *float64
	IntroMarkersAlgorithm  *string
	MarkersSource          *string
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

func (c Candidate) EffectiveIntroSource() string {
	if c.IntroMarkersSource != nil && strings.TrimSpace(*c.IntroMarkersSource) != "" {
		return strings.TrimSpace(*c.IntroMarkersSource)
	}
	if c.IntroStart != nil && c.IntroEnd != nil && c.MarkersSource != nil {
		return strings.TrimSpace(*c.MarkersSource)
	}
	return ""
}

func (c Candidate) HasHigherPriorityIntro(source string) bool {
	if c.IntroStart == nil || c.IntroEnd == nil {
		return false
	}
	return models.MarkerSourcePriority(c.EffectiveIntroSource()) > models.MarkerSourcePriority(source)
}

type Segment struct {
	Start      float64
	End        float64
	Confidence float64
	Algorithm  string
}

type IntroMarkerPatch struct {
	FileID     int
	Start      float64
	End        float64
	Source     string
	Confidence float64
	Algorithm  string
	DetectedAt time.Time
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
}

type RunSummary struct {
	LibrariesScanned             int      `json:"libraries_scanned"`
	FilesConsidered              int      `json:"files_considered"`
	SeasonGroupsConsidered       int      `json:"season_groups_considered"`
	FingerprintsComputed         int      `json:"fingerprints_computed"`
	FingerprintCacheHits         int      `json:"fingerprint_cache_hits"`
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
}
