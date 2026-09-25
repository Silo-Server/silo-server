package markers

import (
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// SegmentPayload is the storage-agnostic per-segment marker write: its bounds
// plus the provenance (provider/confidence/algorithm) of the marker that
// produced it. A merged multi-provider result carries a different provider per
// segment; a single-source result repeats the same provider across segments.
type SegmentPayload struct {
	Start, End *float64
	Ranges     []models.MarkerSegment
	Source     string
	Provider   *string
	Confidence *float64
	Algorithm  string
}

// Present reports whether the segment carries a bound to write.
func (s SegmentPayload) Present() bool { return len(s.Ranges) > 0 || s.Start != nil || s.End != nil }

// CanWriteMarkerUpdate applies source precedence and local detector refinement.
// Online provider selection has already happened in the registry; a refreshed
// selection is authoritative even when a provider's confidence has not risen.
func CanWriteMarkerUpdate(existing, incoming SegmentPayload) bool {
	if !existing.Present() {
		return true
	}
	existingPriority := models.MarkerSourcePriority(existing.Source)
	incomingPriority := models.MarkerSourcePriority(incoming.Source)
	if existingPriority != incomingPriority {
		return incomingPriority > existingPriority
	}
	switch incoming.Source {
	case models.MarkerSourceManual, models.MarkerSourceOnline, models.MarkerSourcePlugin:
		return true
	case models.MarkerSourceScanner:
		if existing.Confidence == nil || existing.Algorithm == "" {
			return true
		}
		// Chromaprint results are scored against the whole season on every
		// analysis, so the latest result of the same detector version
		// replaces the stored one even at lower confidence: a season that no
		// longer agrees must not keep an older, higher score. A plain and a
		// subtitle-refined result of one version count as the same detector,
		// so a file that loses its subtitle also loses the refined score.
		if sameChromaprintVersion(existing.Algorithm, incoming.Algorithm) {
			return incoming.Algorithm != existing.Algorithm ||
				confidenceGreater(incoming.Confidence, existing.Confidence) ||
				confidenceGreater(existing.Confidence, incoming.Confidence) ||
				!sameMarkerRanges(existing, incoming)
		}
		currentRank, nextRank := scannerAlgorithmPriority(existing.Algorithm), scannerAlgorithmPriority(incoming.Algorithm)
		if currentRank != nextRank {
			return nextRank > currentRank
		}
		if confidenceGreater(incoming.Confidence, existing.Confidence) {
			return true
		}
		if confidenceGreater(existing.Confidence, incoming.Confidence) {
			return false
		}
		if incoming.Algorithm != existing.Algorithm {
			return nextRank == 0
		}
		return !sameMarkerRanges(existing, incoming)
	default:
		if confidenceGreater(incoming.Confidence, existing.Confidence) {
			return true
		}
		if confidenceGreater(existing.Confidence, incoming.Confidence) {
			return false
		}
		return !sameMarkerRanges(existing, incoming)
	}
}

// markerRangeTolerance is the largest bound difference that still counts as the
// same occurrence. Provider and detector timestamps are second-resolution but
// arrive through floats, so sub-half-second noise is not a change.
const markerRangeTolerance = 0.5

// sameMarkerRanges reports whether two payloads describe the same occurrences.
// A kind can occur several times, so an equal-confidence update is only
// redundant when every occurrence on one side has a counterpart on the other.
// Comparing just the first occurrence (the pre-multi-occurrence behavior, where
// the singular bounds were the whole answer) discarded corrections to any later
// range; comparing positionally after an exact-value sort paired the wrong
// occurrences whenever sub-tolerance noise reordered two starts, which reported
// a change for an equivalent set and could flip the legacy projection onto the
// other occurrence.
//
// Matching is greedy and tolerance-aware in both bounds, so it does not depend on
// input order at all. Greedy can report a difference where another pairing would
// have matched, which errs toward writing the row rather than skipping a real
// change.
func sameMarkerRanges(existing, incoming SegmentPayload) bool {
	existingRanges := markerRanges(existing)
	incomingRanges := markerRanges(incoming)
	if len(existingRanges) != len(incomingRanges) {
		return false
	}
	matched := make([]bool, len(incomingRanges))
	for _, want := range existingRanges {
		found := false
		for index, have := range incomingRanges {
			if matched[index] {
				continue
			}
			if math.Abs(want.StartSeconds-have.StartSeconds) > markerRangeTolerance ||
				math.Abs(want.EndSeconds-have.EndSeconds) > markerRangeTolerance {
				continue
			}
			matched[index] = true
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

// markerRanges returns a payload's occurrences, treating singular bounds as a
// single occurrence so rows and callers written before multi-occurrence support
// stay comparable.
func markerRanges(payload SegmentPayload) []models.MarkerSegment {
	ranges := slices.Clone(payload.Ranges)
	if len(ranges) == 0 && payload.Start != nil && payload.End != nil {
		ranges = []models.MarkerSegment{{StartSeconds: *payload.Start, EndSeconds: *payload.End}}
	}
	return ranges
}

// sameChromaprintVersion reports whether two scanner algorithms are the plain
// or subtitle-refined result of one Chromaprint version, whose confidence is
// recomputed from the whole season each time it is analyzed.
func sameChromaprintVersion(a, b string) bool {
	const family, refined = "chromaprint:", "dialogue:" //nolint:misspell // Persisted algorithm identifier.
	version := func(algorithm string) (string, bool) {
		rest, ok := strings.CutPrefix(algorithm, family)
		if !ok {
			return "", false
		}
		return strings.TrimPrefix(rest, refined), true
	}
	va, okA := version(a)
	vb, okB := version(b)
	return okA && okB && va == vb
}

// scannerAlgorithmPriority ranks local detector outputs. A superseded version
// ranks below its replacement so re-analysis can overwrite what it wrote.
func scannerAlgorithmPriority(algorithm string) int {
	switch algorithm {
	case "chapter:silence:v2":
		return 40
	case "chapter:v1":
		return 30
	case "chapter:silence:v1": // Extended chapter ends too far; below chapter:v1.
		return 25
	case "episode-version-copy:v1":
		return 24
	case "chromaprint:dialogue:v4": //nolint:misspell // Persisted algorithm identifier.
		return 22
	case "chromaprint:v4":
		return 21
	case "chromaprint:dialogue:v3": //nolint:misspell // Persisted algorithm identifier.
		return 20
	case "chromaprint:v3":
		return 19
	case "chromaprint:dialogue:v2": //nolint:misspell // Persisted algorithm identifier.
		return 18
	case "chromaprint:v2":
		return 17
	case "chromaprint:dialogue:v1": //nolint:misspell // Persisted algorithm identifier.
		return 15
	case "chromaprint:v1":
		return 10
	default:
		return 0
	}
}

// MarkerUpdatePayload is the storage-agnostic shape produced from a provider
// Result. Repositories convert it into their concrete write column set. Source
// is the shared source class (online/scanner/manual/...); each SegmentPayload
// carries its own provider/confidence/algorithm so a merged multi-provider
// result records correct per-segment provenance.
type MarkerUpdatePayload struct {
	Intro              SegmentPayload
	Credits            SegmentPayload
	Recap              SegmentPayload
	Preview            SegmentPayload
	Source             string
	RefreshedProviders []string
}

// HasAnySegment reports whether the payload can change marker state, including
// removing ranges withdrawn by a successfully refreshed provider.
func (p MarkerUpdatePayload) HasAnySegment() bool {
	return p.Intro.Present() || p.Credits.Present() || p.Recap.Present() || p.Preview.Present() || len(p.RefreshedProviders) > 0
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
	payload := MarkerUpdatePayload{Source: result.SourceClass, RefreshedProviders: result.RefreshedProviders}
	for _, m := range result.Markers {
		start := m.Start.Seconds()
		end := m.End.Seconds()
		rangeValue := models.MarkerSegment{Kind: markerKindName(m.Kind), StartSeconds: start, EndSeconds: end}
		if !rangeValue.Valid() {
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
			Ranges:    []models.MarkerSegment{rangeValue},
		}
		if m.Confidence > 0 {
			conf := m.Confidence
			seg.Confidence = &conf
		}
		var target *SegmentPayload
		switch m.Kind {
		case MarkerKindIntro:
			target = &payload.Intro
		case MarkerKindCredits:
			target = &payload.Credits
		case MarkerKindRecap:
			target = &payload.Recap
		case MarkerKindPreview:
			target = &payload.Preview
		}
		if target == nil {
			continue
		}
		if !target.Present() {
			*target = seg
		} else {
			target.Ranges = append(target.Ranges, rangeValue)
		}
	}
	for _, segment := range []*SegmentPayload{&payload.Intro, &payload.Credits, &payload.Recap, &payload.Preview} {
		sort.SliceStable(segment.Ranges, func(i, j int) bool { return segment.Ranges[i].StartSeconds < segment.Ranges[j].StartSeconds })
		if len(segment.Ranges) > 0 {
			segment.Start = &segment.Ranges[0].StartSeconds
			segment.End = &segment.Ranges[0].EndSeconds
		}
	}
	return payload
}

// ApplyResult projects a provider response onto a copy of the file. On-demand
// lookups use the same source precedence as persisted writes without changing
// the stored catalog row.
func ApplyResult(file *models.MediaFile, result Result) *models.MediaFile {
	if file == nil {
		return nil
	}
	next := *file
	byKind := make(map[string][]models.MarkerSegment, 4)
	for _, segment := range models.EffectiveMarkerSegments(file) {
		byKind[segment.Kind] = append(byKind[segment.Kind], segment)
	}
	payload := BuildUpdatePayload(result)
	now := time.Now().UTC()
	targets := []struct {
		kind                        string
		incoming                    SegmentPayload
		start, end                  **float64
		source, provider, algorithm **string
		confidence                  **float64
		detectedAt                  **time.Time
	}{
		{models.MarkerSegmentIntro, payload.Intro, &next.IntroStart, &next.IntroEnd, &next.IntroMarkersSource, &next.IntroMarkersProvider, &next.IntroMarkersAlgorithm, &next.IntroMarkersConfidence, &next.IntroMarkersDetectedAt},
		{models.MarkerSegmentCredits, payload.Credits, &next.CreditsStart, &next.CreditsEnd, &next.CreditsMarkersSource, &next.CreditsMarkersProvider, &next.CreditsMarkersAlgorithm, &next.CreditsMarkersConfidence, &next.CreditsMarkersDetectedAt},
		{models.MarkerSegmentRecap, payload.Recap, &next.RecapStart, &next.RecapEnd, &next.RecapMarkersSource, &next.RecapMarkersProvider, &next.RecapMarkersAlgorithm, &next.RecapMarkersConfidence, &next.RecapMarkersDetectedAt},
		{models.MarkerSegmentPreview, payload.Preview, &next.PreviewStart, &next.PreviewEnd, &next.PreviewMarkersSource, &next.PreviewMarkersProvider, &next.PreviewMarkersAlgorithm, &next.PreviewMarkersConfidence, &next.PreviewMarkersDetectedAt},
	}
	for _, target := range targets {
		existing := SegmentPayload{Start: *target.start, End: *target.end, Ranges: byKind[target.kind], Provider: *target.provider, Confidence: *target.confidence}
		if *target.source != nil {
			existing.Source = **target.source
		} else if existing.Present() && file.MarkersSource != nil {
			existing.Source = *file.MarkersSource
		}
		if *target.algorithm != nil {
			existing.Algorithm = **target.algorithm
		}
		incoming := target.incoming
		if !incoming.Present() {
			if existing.Source != models.MarkerSourceManual && existing.Provider != nil && slices.Contains(result.RefreshedProviders, *existing.Provider) {
				delete(byKind, target.kind)
				*target.start, *target.end, *target.source, *target.provider, *target.algorithm, *target.confidence, *target.detectedAt = nil, nil, nil, nil, nil, nil, nil
			}
			continue
		}
		valid := true
		for _, segment := range incoming.Ranges {
			if file.Duration > 0 && segment.EndSeconds > float64(file.Duration)+1 {
				valid = false
			}
		}
		if !valid || !CanWriteMarkerUpdate(existing, incoming) {
			continue
		}
		byKind[target.kind] = incoming.Ranges
		*target.start, *target.end = incoming.Start, incoming.End
		*target.source, *target.provider, *target.algorithm = &incoming.Source, incoming.Provider, &incoming.Algorithm
		*target.confidence, *target.detectedAt = incoming.Confidence, &now
	}
	next.MarkerSegments = make([]models.MarkerSegment, 0)
	for _, kind := range []string{models.MarkerSegmentIntro, models.MarkerSegmentCredits, models.MarkerSegmentRecap, models.MarkerSegmentPreview} {
		next.MarkerSegments = append(next.MarkerSegments, byKind[kind]...)
	}
	next.MarkerSegments = models.EffectiveMarkerSegments(&next)
	next.MarkersSource, next.MarkersConfidence = nil, nil
	for _, target := range targets {
		if len(byKind[target.kind]) == 0 {
			continue
		}
		source, confidence := *target.source, *target.confidence
		if source == nil {
			source, confidence = file.MarkersSource, file.MarkersConfidence
		}
		if source == nil {
			continue
		}
		if next.MarkersSource == nil || models.MarkerSourcePriority(*source) > models.MarkerSourcePriority(*next.MarkersSource) ||
			(models.MarkerSourcePriority(*source) == models.MarkerSourcePriority(*next.MarkersSource) && confidenceGreater(confidence, next.MarkersConfidence)) {
			next.MarkersSource, next.MarkersConfidence = source, confidence
		}
	}
	return &next
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
