package markers

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// CanWriteMarker reports whether a new marker write should be accepted given
// the existing source/confidence. A strictly higher-priority source always
// wins; an equal-priority source wins only if its confidence is strictly
// higher than what's already stored. Unknown/empty existing source is treated
// as priority zero so any defined source can replace it.
func CanWriteMarker(existingSource *string, existingConfidence *float64, newSource string, newConfidence *float64) bool {
	currentSource := ""
	if existingSource != nil {
		currentSource = *existingSource
	}
	existingPriority := models.MarkerSourcePriority(currentSource)
	newPriority := models.MarkerSourcePriority(newSource)
	if newPriority > existingPriority {
		return true
	}
	if newPriority < existingPriority {
		return false
	}
	if existingConfidence != nil && newConfidence != nil {
		return *newConfidence > *existingConfidence
	}
	return false
}

// SegmentPayload is the storage-agnostic per-segment marker write: its bounds
// plus the provenance (provider/confidence/algorithm) of the marker that
// produced it. A merged multi-provider result carries a different provider per
// segment; a single-source result repeats the same provider across segments.
type SegmentPayload struct {
	Start, End *float64
	Provider   *string
	Confidence *float64
	Algorithm  string
}

// Present reports whether the segment carries a bound to write.
func (s SegmentPayload) Present() bool { return s.Start != nil || s.End != nil }

// MarkerUpdatePayload is the storage-agnostic shape produced from a provider
// Result. Repositories convert it into their concrete write column set. Source
// is the shared source class (online/scanner/manual/...); each SegmentPayload
// carries its own provider/confidence/algorithm so a merged multi-provider
// result records correct per-segment provenance.
type MarkerUpdatePayload struct {
	Intro   SegmentPayload
	Credits SegmentPayload
	Recap   SegmentPayload
	Preview SegmentPayload
	Source  string
}

// HasAnySegment reports whether the payload carries at least one segment
// range. Callers can short-circuit empty writes without touching the DB.
func (p MarkerUpdatePayload) HasAnySegment() bool {
	return p.Intro.Present() || p.Credits.Present() || p.Recap.Present() || p.Preview.Present()
}

// SummaryConfidence returns the highest per-segment confidence present, for the
// legacy shared markers_confidence column. Returns nil when no segment carries
// a confidence.
func (p MarkerUpdatePayload) SummaryConfidence() *float64 {
	var max float64
	found := false
	for _, s := range []SegmentPayload{p.Intro, p.Credits, p.Recap, p.Preview} {
		if s.Confidence != nil && (!found || *s.Confidence > max) {
			max = *s.Confidence
			found = true
		}
	}
	if !found {
		return nil
	}
	return &max
}

// BuildUpdatePayload converts a provider Result into the storage payload. Each
// marker maps to its segment with its own provenance: ProviderID/Algorithm fall
// back to the Result-level values (used for single-provider results), and the
// algorithm finally falls back to external:<source> so every write carries an
// algorithm tag.
func BuildUpdatePayload(result Result) MarkerUpdatePayload {
	payload := MarkerUpdatePayload{Source: result.SourceClass}
	resultAlgorithm := result.Algorithm
	if resultAlgorithm == "" && result.SourceClass != "" {
		resultAlgorithm = "external:" + result.SourceClass
	}
	for _, m := range result.Markers {
		start := m.Start.Seconds()
		end := m.End.Seconds()
		if end <= start {
			continue
		}
		startPtr, endPtr := start, end
		seg := SegmentPayload{
			Start:     &startPtr,
			End:       &endPtr,
			Provider:  markerProvider(m, result),
			Algorithm: markerAlgorithm(m, resultAlgorithm),
		}
		if m.Confidence > 0 {
			conf := m.Confidence
			seg.Confidence = &conf
		}
		switch m.Kind {
		case MarkerKindIntro:
			payload.Intro = seg
		case MarkerKindCredits:
			payload.Credits = seg
		case MarkerKindRecap:
			payload.Recap = seg
		case MarkerKindPreview:
			payload.Preview = seg
		}
	}
	return payload
}

// markerProvider returns the per-marker provider, falling back to the
// Result-level provider (single-provider results), or nil when neither is set.
func markerProvider(m Marker, result Result) *string {
	provider := strings.TrimSpace(m.ProviderID)
	if provider == "" {
		provider = strings.TrimSpace(result.ProviderID)
	}
	if provider == "" {
		return nil
	}
	return &provider
}

// markerAlgorithm returns the per-marker algorithm, falling back to the
// already-resolved Result-level algorithm.
func markerAlgorithm(m Marker, resultAlgorithm string) string {
	if a := strings.TrimSpace(m.Algorithm); a != "" {
		return a
	}
	return resultAlgorithm
}
