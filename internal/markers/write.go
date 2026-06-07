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
//
// A manual write is the exception: an explicit human edit is authoritative and
// always applies at equal-or-higher priority (last-writer-wins). Without this,
// correcting a previously hand-set marker — whose confidence is fixed at 1.0 —
// would be silently rejected by the strictly-higher-confidence rule, leaving
// users unable to fix their own (or each other's) edits.
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
	if newSource == models.MarkerSourceManual {
		return true
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
	Source     string
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

// SummarySource returns the highest-priority source class present in the
// per-segment payloads, using confidence as the tie-breaker. This keeps the
// legacy shared markers_source column coherent even when individual segment
// provenance differs.
func (p MarkerUpdatePayload) SummarySource() string {
	source := strings.TrimSpace(p.Source)
	confidence := p.SummaryConfidence()
	found := source != ""
	for _, s := range []SegmentPayload{p.Intro, p.Credits, p.Recap, p.Preview} {
		if !s.Present() || strings.TrimSpace(s.Source) == "" {
			continue
		}
		if !found ||
			models.MarkerSourcePriority(s.Source) > models.MarkerSourcePriority(source) ||
			(models.MarkerSourcePriority(s.Source) == models.MarkerSourcePriority(source) && confidenceGreater(s.Confidence, confidence)) {
			source = s.Source
			confidence = s.Confidence
			found = true
		}
	}
	return source
}

// BuildUpdatePayload converts a provider Result into the storage payload. Each
// marker maps to its segment with its own provenance: ProviderID/Algorithm fall
// back to the Result-level values (used for single-provider results), and the
// algorithm finally falls back to external:<source> so every write carries an
// algorithm tag.
func BuildUpdatePayload(result Result) MarkerUpdatePayload {
	payload := MarkerUpdatePayload{Source: result.SourceClass}
	for _, m := range result.Markers {
		start := m.Start.Seconds()
		end := m.End.Seconds()
		if end <= start {
			continue
		}
		startPtr, endPtr := start, end
		source := markerSource(m, result)
		seg := SegmentPayload{
			Start:     &startPtr,
			End:       &endPtr,
			Source:    source,
			Provider:  markerProvider(m, result),
			Algorithm: markerAlgorithm(m, result.Algorithm, source),
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

func markerSource(m Marker, result Result) string {
	source := strings.TrimSpace(m.SourceClass)
	if source == "" {
		source = strings.TrimSpace(result.SourceClass)
	}
	return source
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
func markerAlgorithm(m Marker, resultAlgorithm, source string) string {
	if a := strings.TrimSpace(m.Algorithm); a != "" {
		return a
	}
	if strings.TrimSpace(resultAlgorithm) == "" && strings.TrimSpace(source) != "" {
		return "external:" + source
	}
	return resultAlgorithm
}

func confidenceGreater(a, b *float64) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return *a > *b
}
