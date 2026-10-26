package intromarkers

import (
	"math"
	"sort"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// End credits on video are text on true black ("lettered"), plain true black
// between cards, or text on a flat light background ("card"). Dense credits
// and logos on black can fill too much of the picture to pass as a black
// background; they only bridge the gaps between text keyframes. The rules
// below were tuned on frame-checked episodes and movies; every early start
// they produced was checked against frames and cut no story. The notes on
// each threshold name the false positive it removed.
const (
	// blackLevelPercentile is the share of keyframes whose YLOW sets the
	// file's black level. In a long tail it skips a few keyframes of broken
	// or faded pictures. A tail of fewer than 200 keyframes uses its
	// darkest: it may hold only a few black keyframes, and those set the
	// level.
	blackLevelPercentile = 0.01
	// maxBlackLevel caps the black level, so a tail with no dark keyframes
	// cannot count a dim scene as black.
	maxBlackLevel = 30.0

	// A black background: at least blackCoverage percent of the picture
	// below luma 32, its 10th percentile at the file's black level (within
	// blackLevelMargin), almost no saturation, and at least
	// strictBlackCoverage percent below the strict threshold for the file's
	// black level. Dark scenes pass the loose test but only 26–58 percent of
	// their pixels pass the strict one; text on black keeps 82–96 percent.
	blackCoverage       = 85
	blackLevelMargin    = 2.0
	maxBlackSaturation  = 10.0
	strictBlackCoverage = 75
	// letteredContrast is the brightest-to-background difference that shows
	// text on a black background.
	letteredContrast = 60.0
	// mostlyBlackCoverage is the least share below luma 32 of a keyframe
	// that passes every other black background test. Dense credit columns
	// kept 79–84 percent of the picture below luma 32.
	mostlyBlackCoverage = 70

	// A card: a flat background (its 10th and 90th luma percentiles within
	// cardFlatness), text that stands out from it by cardContrast, modest
	// saturation, and a background clearly lighter than black. A dark scene
	// fails the last test, which kept night scenes out of "card".
	cardFlatness       = 8.0
	cardContrast       = 60.0
	maxCardSaturation  = 96.0
	cardBackgroundLift = 24.0
)

// The stats thresholds the tail pass measures, in the order FrameStats.PBlack
// reports them. Which strict threshold applies depends on the file's black
// level: it sits about four levels above black.
var creditsBlackThresholds = []int{20, 26, 32}

const (
	strictThreshold20MaxBlackLevel = 16.0
	strictThreshold26MaxBlackLevel = 22.0
	// pblackLoose is the PBlack slot of luma 32.
	pblackLoose = 2
)

// keyframeClass is what a tail keyframe shows.
type keyframeClass int

const (
	keyframeContent keyframeClass = iota
	// keyframeBlack is a true-black keyframe without text.
	keyframeBlack
	// keyframeLettered is text on a true-black background.
	keyframeLettered
	// keyframeCard is text on a flat, lighter background.
	keyframeCard
	// keyframeMostlyBlack is a black background with too much bright
	// picture on it to call black: dense credits, logos, or artwork. It can
	// join credits keyframes but neither starts nor ends a run, and does not
	// count toward a run's text share.
	keyframeMostlyBlack
)

func (c keyframeClass) text() bool { return c == keyframeLettered || c == keyframeCard }

// creditLike reports whether a keyframe can belong to a credits sequence.
func (c keyframeClass) creditLike() bool {
	return c.text() || c == keyframeBlack || c == keyframeMostlyBlack
}

// creditsKeyframe is a classified keyframe at an absolute media time.
type creditsKeyframe struct {
	Seconds float64
	Class   keyframeClass
}

// classifyKeyframes classifies the tail's keyframes against the tail's own
// black level. Frames without the three black measurements are dropped.
func classifyKeyframes(frames []mediasample.FrameStats) []creditsKeyframe {
	usable := make([]mediasample.FrameStats, 0, len(frames))
	for _, frame := range frames {
		if len(frame.PBlack) == len(creditsBlackThresholds) {
			usable = append(usable, frame)
		}
	}
	if len(usable) == 0 {
		return nil
	}
	blackLevel := tailBlackLevel(usable)
	keyframes := make([]creditsKeyframe, len(usable))
	for i, frame := range usable {
		keyframes[i] = creditsKeyframe{Seconds: frame.Seconds, Class: classifyKeyframe(frame, blackLevel)}
	}
	return keyframes
}

// tailBlackLevel is the tail's black level: the YLOW of the keyframe ranked
// len/100 from the darkest (at least the darkest), capped at maxBlackLevel.
func tailBlackLevel(frames []mediasample.FrameStats) float64 {
	lows := make([]float64, len(frames))
	for i, frame := range frames {
		lows[i] = float64(frame.YLow)
	}
	sort.Float64s(lows)
	index := max(0, int(float64(len(lows))*blackLevelPercentile)-1)
	return math.Min(maxBlackLevel, lows[index])
}

func classifyKeyframe(frame mediasample.FrameStats, blackLevel float64) keyframeClass {
	yMin, yLow, yHigh, yMax := float64(frame.YMin), float64(frame.YLow), float64(frame.YHigh), float64(frame.YMax)
	strict := frame.PBlack[pblackLoose]
	switch {
	case blackLevel <= strictThreshold20MaxBlackLevel:
		strict = frame.PBlack[0]
	case blackLevel <= strictThreshold26MaxBlackLevel:
		strict = frame.PBlack[1]
	}
	blackFloor := yLow <= blackLevel+blackLevelMargin &&
		float64(frame.SatLow) < maxBlackSaturation &&
		strict >= strictBlackCoverage
	if blackFloor && frame.PBlack[pblackLoose] >= blackCoverage {
		if yMax-yLow >= letteredContrast {
			return keyframeLettered
		}
		return keyframeBlack
	}
	if yHigh-yLow <= cardFlatness &&
		math.Max(yMax-yHigh, yLow-yMin) >= cardContrast &&
		float64(frame.SatAvg) < maxCardSaturation &&
		yLow >= blackLevel+cardBackgroundLift {
		return keyframeCard
	}
	if blackFloor && frame.PBlack[pblackLoose] >= mostlyBlackCoverage {
		return keyframeMostlyBlack
	}
	return keyframeContent
}

// Credits runs. A run starts at a text keyframe and extends over credit-like
// keyframes no more than creditsMergeGapSeconds apart, ending at its last
// text keyframe. It counts as credits when it spans at least
// creditsMinimumSeconds, holds at least minimumRunTextKeyframes text
// keyframes, and text makes up more than half of its keyframes.
const (
	creditsMergeGapSeconds  = 20.0
	minimumRunTextKeyframes = 3
)

// creditsRun is a stretch of keyframes that looks like credits. First and
// Last index its first and last keyframes.
type creditsRun struct {
	Start, End  float64
	First, Last int
	Text        int
	Lettered    int
}

// buildRuns returns the qualifying credits runs among keyframes, in order.
func buildRuns(keyframes []creditsKeyframe) []creditsRun {
	var runs []creditsRun
	for i := 0; i < len(keyframes); {
		if !keyframes[i].Class.text() {
			i++
			continue
		}
		first, lastCredit, lastText := i, i, i
		for j := i + 1; j < len(keyframes); j++ {
			gap := keyframes[j].Seconds - keyframes[lastCredit].Seconds
			if gap > creditsMergeGapSeconds {
				break
			}
			if keyframes[j].Class.creditLike() {
				lastCredit = j
				if keyframes[j].Class.text() {
					lastText = j
				}
			}
		}
		run := creditsRun{
			Start: keyframes[first].Seconds,
			End:   keyframes[lastText].Seconds,
			First: first,
			Last:  lastText,
		}
		span := 0
		for _, keyframe := range keyframes[first : lastText+1] {
			if keyframe.Class.text() {
				run.Text++
			}
			if keyframe.Class == keyframeLettered {
				run.Lettered++
			}
			if keyframe.Class != keyframeMostlyBlack {
				span++
			}
		}
		if run.End-run.Start >= creditsMinimumSeconds && run.Text >= minimumRunTextKeyframes && 2*run.Text > span {
			runs = append(runs, run)
		}
		i = lastText + 1
	}
	return runs
}

// clusterRuns joins runs no more than creditsMergeGapSeconds apart. A
// cluster keeps its first run's start and index, its last run's end, and the
// text counts of all of them.
func clusterRuns(runs []creditsRun) []creditsRun {
	var clusters []creditsRun
	for _, run := range runs {
		if n := len(clusters); n > 0 && run.Start-clusters[n-1].End <= creditsMergeGapSeconds {
			cluster := &clusters[n-1]
			cluster.End = math.Max(cluster.End, run.End)
			cluster.Last = run.Last
			cluster.Text += run.Text
			cluster.Lettered += run.Lettered
			continue
		}
		clusters = append(clusters, run)
	}
	return clusters
}
