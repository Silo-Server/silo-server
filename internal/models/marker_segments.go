package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"time"
)

const (
	MarkerSegmentIntro   = "intro"
	MarkerSegmentCredits = "credits"
	MarkerSegmentRecap   = "recap"
	MarkerSegmentPreview = "preview"
)

// MarkerSegment is one occurrence of a skippable part of a media file.
type MarkerSegment struct {
	Kind         string  `json:"kind"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
}

func (s MarkerSegment) Valid() bool {
	return (s.Kind == MarkerSegmentIntro || s.Kind == MarkerSegmentCredits || s.Kind == MarkerSegmentRecap || s.Kind == MarkerSegmentPreview) &&
		!math.IsNaN(s.StartSeconds) && !math.IsInf(s.StartSeconds, 0) &&
		!math.IsNaN(s.EndSeconds) && !math.IsInf(s.EndSeconds, 0) &&
		s.StartSeconds >= 0 && s.EndSeconds > s.StartSeconds
}

// EffectiveMarkerSegments reads all occurrences, falling back independently
// for each kind to the singular fields used by older files and clients.
func EffectiveMarkerSegments(file *MediaFile) []MarkerSegment {
	segments := make([]MarkerSegment, 0)
	if file == nil {
		return segments
	}
	present := make(map[string]bool, 4)
	for _, s := range file.MarkerSegments {
		if s.Valid() {
			segments = append(segments, s)
			present[s.Kind] = true
		}
	}
	for _, legacy := range []struct {
		kind       string
		start, end *float64
	}{
		{MarkerSegmentIntro, file.IntroStart, file.IntroEnd},
		{MarkerSegmentCredits, file.CreditsStart, file.CreditsEnd},
		{MarkerSegmentRecap, file.RecapStart, file.RecapEnd},
		{MarkerSegmentPreview, file.PreviewStart, file.PreviewEnd},
	} {
		if present[legacy.kind] || legacy.start == nil || legacy.end == nil {
			continue
		}
		s := MarkerSegment{Kind: legacy.kind, StartSeconds: *legacy.start, EndSeconds: *legacy.end}
		if s.Valid() {
			segments = append(segments, s)
		}
	}
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].StartSeconds != segments[j].StartSeconds {
			return segments[i].StartSeconds < segments[j].StartSeconds
		}
		if segments[i].EndSeconds != segments[j].EndSeconds {
			return segments[i].EndSeconds < segments[j].EndSeconds
		}
		return segments[i].Kind < segments[j].Kind
	})
	return segments
}

// MarkerFileIdentity identifies the bytes, duration and catalog identity a
// marker lookup described. It excludes mutable marker and probe timestamps.
func MarkerFileIdentity(file *MediaFile) string {
	if file == nil {
		return ""
	}
	var modifiedAt *time.Time
	if file.FileModifiedAt != nil {
		normalized := NormalizeFileModifiedAt(*file.FileModifiedAt).UTC()
		modifiedAt = &normalized
	}
	identity, _ := json.Marshal([]any{
		file.ID, file.FileHash, file.FileSize, modifiedAt, file.Duration,
		file.ContentID, file.EpisodeID, file.ExtraID, file.SeasonNumber, file.EpisodeNumber,
	})
	hash := sha256.Sum256(identity)
	return hex.EncodeToString(hash[:])
}

// MarkerSnapshotExpired marks a marker image deferred because only an expired
// provider snapshot knew its range. A new lookup clears it.
const MarkerSnapshotExpired = "marker_snapshot_expired"

// MarkerThumbnail is internal extraction state, separate from embedded chapters.
// Identity binds the occurrence to the file bytes and source-time range.
type MarkerThumbnail struct {
	MediaChapter
	Kind     string `json:"kind"`
	Identity string `json:"identity"`
}

func MarkerThumbnailIdentity(file *MediaFile, segment MarkerSegment) string {
	value, _ := json.Marshal([]any{MarkerFileIdentity(file), file.FilePath, segment})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// EffectiveMarkerThumbnails never exposes metadata from an old file or range.
func EffectiveMarkerThumbnails(file *MediaFile) []MarkerThumbnail {
	out := make([]MarkerThumbnail, 0)
	if file == nil {
		return out
	}
	for _, segment := range EffectiveMarkerSegments(file) {
		// Duration is truncated to whole seconds; marker writers accept ranges
		// up to one second past it, so markers in the final second keep a preview.
		if file.Duration <= 0 || segment.StartSeconds >= float64(file.Duration)+1 || segment.EndSeconds > float64(file.Duration)+1 {
			continue
		}
		identity := MarkerThumbnailIdentity(file, segment)
		thumbnail := MarkerThumbnail{Kind: segment.Kind, Identity: identity, MediaChapter: MediaChapter{StartSeconds: segment.StartSeconds, EndSeconds: segment.EndSeconds, Source: "marker"}}
		for _, saved := range file.MarkerThumbnails {
			if saved.Identity == identity {
				thumbnail.MediaChapter = saved.MediaChapter
				thumbnail.StartSeconds = segment.StartSeconds
				thumbnail.EndSeconds = segment.EndSeconds
				thumbnail.Source = "marker"
				break
			}
		}
		out = append(out, thumbnail)
	}
	return out
}
