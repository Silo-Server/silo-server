package intromarkers

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// markerKind is a marker segment local analysis detects. The zero value is no
// kind, so a patch that forgets to name one is rejected rather than written
// as an intro.
type markerKind int

const (
	kindIntro markerKind = iota + 1
	kindCredits
)

func (k markerKind) String() string {
	switch k {
	case kindIntro:
		return "intro"
	case kindCredits:
		return "credits"
	default:
		return "unknown"
	}
}

// candidateMarker is a file's stored marker of one kind, as loaded with its
// candidate.
type candidateMarker struct {
	Start      *float64
	End        *float64
	Source     *string
	Confidence *float64
	Algorithm  *string
}

func (m candidateMarker) present() bool {
	return m.Start != nil && m.End != nil
}

// marker returns the candidate's stored marker of kind.
func (c Candidate) marker(kind markerKind) candidateMarker {
	switch kind {
	case kindIntro:
		return candidateMarker{
			Start:      c.IntroStart,
			End:        c.IntroEnd,
			Source:     c.IntroMarkersSource,
			Confidence: c.IntroMarkersConfidence,
			Algorithm:  c.IntroMarkersAlgorithm,
		}
	case kindCredits:
		return candidateMarker{
			Start:      c.CreditsStart,
			End:        c.CreditsEnd,
			Source:     c.CreditsMarkersSource,
			Confidence: c.CreditsMarkersConfidence,
			Algorithm:  c.CreditsMarkersAlgorithm,
		}
	default:
		return candidateMarker{}
	}
}

// effectiveSource is the source of the candidate's marker of kind. A marker
// written before per-segment provenance carries only the file's shared
// markers_source.
func (c Candidate) effectiveSource(kind markerKind) string {
	m := c.marker(kind)
	if m.Source != nil && strings.TrimSpace(*m.Source) != "" {
		return strings.TrimSpace(*m.Source)
	}
	if m.present() && c.MarkersSource != nil {
		return strings.TrimSpace(*c.MarkersSource)
	}
	return ""
}

// hasHigherPriority reports whether the candidate's marker of kind came from
// a source that outranks source.
func (c Candidate) hasHigherPriority(kind markerKind, source string) bool {
	if !c.marker(kind).present() {
		return false
	}
	return models.MarkerSourcePriority(c.effectiveSource(kind)) > models.MarkerSourcePriority(source)
}

// ownsMarker reports whether local analysis may write the candidate's marker
// of kind: it has none, or the one it has came from local analysis.
func (c Candidate) ownsMarker(kind markerKind) bool {
	if c.hasHigherPriority(kind, models.MarkerSourceScanner) {
		return false
	}
	return !c.marker(kind).present() || c.effectiveSource(kind) == models.MarkerSourceScanner
}

// ownCandidates returns the candidates whose marker of kind local analysis
// may write. Each kind is judged on its own, so a file whose intro came from
// a higher-priority source can still get a local credits marker.
func ownCandidates(candidates []Candidate, kind markerKind) []Candidate {
	remaining := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ownsMarker(kind) {
			remaining = append(remaining, candidate)
		}
	}
	return remaining
}
