package markers

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// OverlayOnline returns a copy of stored with the online and plugin markers of
// view laid over it. view is an earlier on-demand read of the same file whose
// provider markers are never saved, so a fresh read of the stored row lacks
// them. Only those provider markers are taken from view: every other kind
// comes from stored, so an edit or clear made after view was read is not
// undone. A provider marker replaces a stored one by the same precedence
// ApplyResult used to build view, so a manual marker keeps its place while an
// online marker left over from stored mode yields to the fresh lookup. A
// provider marker left over from stored mode that view lacks was withdrawn by
// its refreshed provider, so it is dropped here too; on_demand mode saves no
// provider markers, so none can have been stored since view was read.
//
// It returns stored unchanged when view is nil or describes another file, and
// nil when stored is nil.
func OverlayOnline(stored, view *models.MediaFile) *models.MediaFile {
	if stored == nil || view == nil || view.ID != stored.ID {
		return stored
	}
	merged := *stored
	storedByKind, viewByKind := segmentsByKind(stored), segmentsByKind(view)
	viewFields := fileSegmentFields(view)
	changed := false
	for i, target := range fileSegmentFields(&merged) {
		from := viewFields[i]
		online := from.payload(view, viewByKind[from.kind])
		if !online.Present() {
			if existing := target.payload(stored, storedByKind[target.kind]); isProviderMarker(existing) && existing.Provider != nil {
				target.clear()
				delete(storedByKind, target.kind)
				changed = true
			}
			continue
		}
		// ApplyResult pins the source on every provider marker it lays
		// over. A kind without its own source came from storage, and
		// view's recomputed file-level source says nothing about it.
		if *from.source == nil || !isProviderMarker(online) {
			continue
		}
		if !CanWriteMarkerUpdate(target.payload(stored, storedByKind[target.kind]), online) {
			continue
		}
		target.copyFrom(from)
		storedByKind[target.kind] = viewByKind[from.kind]
		changed = true
	}
	if !changed {
		return &merged
	}
	setSegments(&merged, storedByKind)
	summarizeSources(&merged, stored.MarkersSource, stored.MarkersConfidence)
	return &merged
}

// isProviderMarker reports whether p came from an online or plugin provider.
func isProviderMarker(p SegmentPayload) bool {
	return p.Present() && (p.Source == models.MarkerSourceOnline || p.Source == models.MarkerSourcePlugin)
}

// segmentFields addresses one marker kind's columns on a file.
type segmentFields struct {
	kind                        string
	start, end                  **float64
	source, provider, algorithm **string
	confidence                  **float64
	detectedAt                  **time.Time
}

// fileSegmentFields returns the per-kind marker columns of f, in the order
// marker segments are stored.
func fileSegmentFields(f *models.MediaFile) []segmentFields {
	return []segmentFields{
		{models.MarkerSegmentIntro, &f.IntroStart, &f.IntroEnd, &f.IntroMarkersSource, &f.IntroMarkersProvider, &f.IntroMarkersAlgorithm, &f.IntroMarkersConfidence, &f.IntroMarkersDetectedAt},
		{models.MarkerSegmentCredits, &f.CreditsStart, &f.CreditsEnd, &f.CreditsMarkersSource, &f.CreditsMarkersProvider, &f.CreditsMarkersAlgorithm, &f.CreditsMarkersConfidence, &f.CreditsMarkersDetectedAt},
		{models.MarkerSegmentRecap, &f.RecapStart, &f.RecapEnd, &f.RecapMarkersSource, &f.RecapMarkersProvider, &f.RecapMarkersAlgorithm, &f.RecapMarkersConfidence, &f.RecapMarkersDetectedAt},
		{models.MarkerSegmentPreview, &f.PreviewStart, &f.PreviewEnd, &f.PreviewMarkersSource, &f.PreviewMarkersProvider, &f.PreviewMarkersAlgorithm, &f.PreviewMarkersConfidence, &f.PreviewMarkersDetectedAt},
	}
}

// payload returns the kind's stored marker for precedence checks. A marker
// written before per-segment provenance carries only file's shared source.
func (s segmentFields) payload(file *models.MediaFile, ranges []models.MarkerSegment) SegmentPayload {
	p := SegmentPayload{Start: *s.start, End: *s.end, Ranges: ranges, Provider: *s.provider, Confidence: *s.confidence}
	if *s.source != nil {
		p.Source = **s.source
	} else if p.Present() && file.MarkersSource != nil {
		p.Source = *file.MarkersSource
	}
	if *s.algorithm != nil {
		p.Algorithm = **s.algorithm
	}
	return p
}

func (s segmentFields) copyFrom(from segmentFields) {
	*s.start, *s.end = *from.start, *from.end
	*s.source, *s.provider, *s.algorithm = *from.source, *from.provider, *from.algorithm
	*s.confidence, *s.detectedAt = *from.confidence, *from.detectedAt
}

func (s segmentFields) clear() {
	*s.start, *s.end, *s.source, *s.provider, *s.algorithm, *s.confidence, *s.detectedAt = nil, nil, nil, nil, nil, nil, nil
}

// segmentsByKind groups the file's effective marker occurrences by kind.
func segmentsByKind(file *models.MediaFile) map[string][]models.MarkerSegment {
	byKind := make(map[string][]models.MarkerSegment, 4)
	for _, segment := range models.EffectiveMarkerSegments(file) {
		byKind[segment.Kind] = append(byKind[segment.Kind], segment)
	}
	return byKind
}

// setSegments replaces the file's marker occurrences with byKind.
func setSegments(file *models.MediaFile, byKind map[string][]models.MarkerSegment) {
	file.MarkerSegments = make([]models.MarkerSegment, 0)
	for _, kind := range []string{models.MarkerSegmentIntro, models.MarkerSegmentCredits, models.MarkerSegmentRecap, models.MarkerSegmentPreview} {
		file.MarkerSegments = append(file.MarkerSegments, byKind[kind]...)
	}
	file.MarkerSegments = models.EffectiveMarkerSegments(file)
}

// summarizeSources recomputes the legacy file-level markers_source and
// markers_confidence as the strongest source among the kinds present. A kind
// without its own source falls back to fallbackSource, the file's previous
// shared source.
func summarizeSources(file *models.MediaFile, fallbackSource *string, fallbackConfidence *float64) {
	present := make(map[string]bool, 4)
	for _, segment := range file.MarkerSegments {
		present[segment.Kind] = true
	}
	file.MarkersSource, file.MarkersConfidence = nil, nil
	for _, target := range fileSegmentFields(file) {
		if !present[target.kind] {
			continue
		}
		source, confidence := *target.source, *target.confidence
		if source == nil {
			source, confidence = fallbackSource, fallbackConfidence
		}
		if source == nil {
			continue
		}
		if file.MarkersSource == nil || models.MarkerSourcePriority(*source) > models.MarkerSourcePriority(*file.MarkersSource) ||
			(models.MarkerSourcePriority(*source) == models.MarkerSourcePriority(*file.MarkersSource) && confidenceGreater(confidence, file.MarkersConfidence)) {
			file.MarkersSource, file.MarkersConfidence = source, confidence
		}
	}
}
