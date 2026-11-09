package markers

import (
	"errors"
	"fmt"
	"math"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Credits placement bounds, validated against frame-checked episodes and
// movies. Credits start in the file's tail window: the last TailSeconds, or
// TailFraction of a shorter file. Local detection and every other marker
// source share these bounds, since players open Up Next at the credits start.
const (
	CreditsMinimumSeconds = 15.0

	EpisodeCreditsTailSeconds    = 450.0
	EpisodeCreditsTailFraction   = 0.4
	EpisodeCreditsMaximumSeconds = 450.0

	MovieCreditsTailSeconds    = 900.0
	MovieCreditsTailFraction   = 0.25
	MovieCreditsMaximumSeconds = 900.0
)

// CreditsLimits are the tail window and length bounds for one kind of file.
type CreditsLimits struct {
	TailSeconds  float64
	TailFraction float64
	MinSeconds   float64
	MaxSeconds   float64
}

// CreditsLimitsFor returns the bounds for a movie or an episode.
func CreditsLimitsFor(isMovie bool) CreditsLimits {
	if isMovie {
		return CreditsLimits{
			TailSeconds:  MovieCreditsTailSeconds,
			TailFraction: MovieCreditsTailFraction,
			MinSeconds:   CreditsMinimumSeconds,
			MaxSeconds:   MovieCreditsMaximumSeconds,
		}
	}
	return CreditsLimits{
		TailSeconds:  EpisodeCreditsTailSeconds,
		TailFraction: EpisodeCreditsTailFraction,
		MinSeconds:   CreditsMinimumSeconds,
		MaxSeconds:   EpisodeCreditsMaximumSeconds,
	}
}

// CreditsLimitsForFile returns the bounds for file: episode bounds when it
// belongs to an episode, movie bounds otherwise. The episode number parsed
// from the file name counts too, because the scanner writes sidecar markers
// before the file is linked to its episode.
func CreditsLimitsForFile(file *models.MediaFile) CreditsLimits {
	isEpisode := file != nil && (file.EpisodeID != "" || file.EpisodeNumber > 0)
	return CreditsLimitsFor(!isEpisode)
}

// WindowStart is where the tail window of a file of duration seconds begins.
func (l CreditsLimits) WindowStart(duration float64) float64 {
	if duration <= 0 {
		return 0
	}
	return duration - math.Min(l.TailSeconds, l.TailFraction*duration)
}

// CheckCreditsPlacement reports why credits from start to end cannot belong
// to a file of duration seconds whose intro ends at introEnd (nil when it has
// none), or nil when they can. An unknown duration (zero or less) cannot be
// judged and passes.
func (l CreditsLimits) CheckCreditsPlacement(start, end, duration float64, introEnd *float64) error {
	if duration <= 0 {
		return nil
	}
	if windowStart := l.WindowStart(duration); start < windowStart {
		return fmt.Errorf("credits must start at or after %s, in the end-credits window", formatClock(windowStart))
	}
	if introEnd != nil && start < *introEnd {
		return errors.New("credits cannot start before the intro ends")
	}
	if length := end - start; length < l.MinSeconds || length > l.MaxSeconds {
		return fmt.Errorf("credits must run between %s and %s", formatClock(l.MinSeconds), formatClock(l.MaxSeconds))
	}
	return nil
}

// KeepPlaced returns the credits in ranges that pass CheckCreditsPlacement.
func (l CreditsLimits) KeepPlaced(ranges []models.MarkerSegment, duration float64, introEnd *float64) []models.MarkerSegment {
	kept := make([]models.MarkerSegment, 0, len(ranges))
	for _, segment := range ranges {
		if l.CheckCreditsPlacement(segment.StartSeconds, segment.EndSeconds, duration, introEnd) == nil {
			kept = append(kept, segment)
		}
	}
	return kept
}

// formatClock renders seconds as h:mm:ss, or m:ss under an hour, rounding up
// so the time it names is never before the bound it describes.
func formatClock(seconds float64) string {
	total := int(math.Ceil(seconds))
	h, m, s := total/3600, total/60%60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
