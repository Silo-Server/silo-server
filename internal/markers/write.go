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
	return true
}

// MarkerUpdatePayload is the storage-agnostic shape produced from a provider
// Result. Repositories convert it into their concrete write column set.
// Pointer fields are nil when the result didn't carry that segment kind.
type MarkerUpdatePayload struct {
	IntroStart, IntroEnd     *float64
	CreditsStart, CreditsEnd *float64
	RecapStart, RecapEnd     *float64
	PreviewStart, PreviewEnd *float64
	Source                   string
	Provider                 *string
	Confidence               *float64
	Algorithm                string
}

// HasAnySegment reports whether the payload carries at least one segment
// range. Callers can short-circuit empty writes without touching the DB.
func (p MarkerUpdatePayload) HasAnySegment() bool {
	return p.IntroStart != nil || p.IntroEnd != nil ||
		p.CreditsStart != nil || p.CreditsEnd != nil ||
		p.RecapStart != nil || p.RecapEnd != nil ||
		p.PreviewStart != nil || p.PreviewEnd != nil
}

// BuildUpdatePayload converts a provider Result into the storage payload.
// All four segment kinds in the result map to the corresponding *Start/*End
// pointer pair; absent kinds remain nil. The Result's Algorithm field is
// authoritative; the falls back to `external:<source>` if absent so writes
// always carry an algorithm tag for provenance.
//
// Confidence: the highest per-marker confidence in the result is promoted to
// the shared write-time confidence. Per-segment confidence values are not
// preserved individually — the write path uses a single confidence per
// upsert, and providers today return uniform confidence across all segments
// of a single fetch.
func BuildUpdatePayload(result Result) MarkerUpdatePayload {
	payload := MarkerUpdatePayload{
		Source:    result.SourceClass,
		Algorithm: result.Algorithm,
	}
	if payload.Algorithm == "" && payload.Source != "" {
		payload.Algorithm = "external:" + payload.Source
	}
	if provider := strings.TrimSpace(result.ProviderID); provider != "" {
		payload.Provider = &provider
	}

	var maxConfidence float64
	for _, m := range result.Markers {
		start := m.Start.Seconds()
		end := m.End.Seconds()
		if end <= start {
			continue
		}
		startPtr, endPtr := start, end
		switch m.Kind {
		case MarkerKindIntro:
			payload.IntroStart = &startPtr
			payload.IntroEnd = &endPtr
		case MarkerKindCredits:
			payload.CreditsStart = &startPtr
			payload.CreditsEnd = &endPtr
		case MarkerKindRecap:
			payload.RecapStart = &startPtr
			payload.RecapEnd = &endPtr
		case MarkerKindPreview:
			payload.PreviewStart = &startPtr
			payload.PreviewEnd = &endPtr
		}
		if m.Confidence > maxConfidence {
			maxConfidence = m.Confidence
		}
	}
	if maxConfidence > 0 {
		payload.Confidence = &maxConfidence
	}
	return payload
}
