package mediasample

import (
	"math"
	"regexp"
	"sort"
	"strconv"
)

// Interval is a span of the input in absolute media seconds. End is zero when
// ffmpeg never reported an end, such as a silence still open when the sampled
// span ended; Start is still a usable boundary.
type Interval struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// silenceEventPattern matches silencedetect's silence_start and silence_end
// values. ffmpeg prints them with av_ts2timestr ("%.6g"), so a value can be
// negative (a window that opens in silence reports a start just before 0) or
// use an exponent.
var silenceEventPattern = regexp.MustCompile(`silence_(start|end):\s*([-+]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][-+]?[0-9]+)?)`)

// silenceParser reads silencedetect events in log order and pairs each
// silence_end with the most recent unmatched silence_start. ffmpeg reports
// times relative to the sampled window; a negative start is clamped to the
// window start before offset (the window start in media seconds) is added. A
// silence_end with no unmatched start is ignored. A start that never gets an
// end is kept with a zero End.
type silenceParser struct {
	offset    float64
	intervals []Interval
	open      []int
}

func newSilenceParser(offset float64) *silenceParser {
	return &silenceParser{offset: offset}
}

func (p *silenceParser) line(line string) {
	for _, match := range silenceEventPattern.FindAllStringSubmatch(line, -1) {
		value, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			continue
		}
		seconds := p.offset + math.Max(0, value)
		if match[1] == "start" {
			p.open = append(p.open, len(p.intervals))
			p.intervals = append(p.intervals, Interval{Start: seconds})
			continue
		}
		if len(p.open) == 0 {
			continue
		}
		last := p.open[len(p.open)-1]
		p.open = p.open[:len(p.open)-1]
		p.intervals[last].End = math.Max(seconds, p.intervals[last].Start)
	}
}

// result returns the silences ordered by start.
func (p *silenceParser) result() []Interval {
	intervals := append([]Interval(nil), p.intervals...)
	sort.SliceStable(intervals, func(i, j int) bool {
		return intervals[i].Start < intervals[j].Start
	})
	return intervals
}
