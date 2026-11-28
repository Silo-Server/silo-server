package intromarkers

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// The intro fingerprint pass also runs silencedetect over the opening
// window. The filter passes the audio through unchanged, so the Chromaprint
// points are byte for byte what a fingerprint-only run produces and cached
// fingerprints stay valid. The silences let pair matches reject pure-silence
// runs, split near-zero studio bumpers off the theme, and trim consensus edges
// off trailing fades and cold-open transitions.
const (
	// headSilenceNoiseDB and headSilenceMinSeconds are the silencedetect
	// parameters of the fingerprint pass.
	headSilenceNoiseDB       = -50
	headSilenceMinSeconds    = 0.3
	chromaprintSilenceFormat = "chromaprint+silence:v1"
)

// headSilenceParams is the silence output of the intro fingerprint pass.
// Silences are detected on the stereo downmix, which the split and trim
// bounds below were tuned on: a 5.1 opening measured on its own channels
// reports a fraction of them.
func headSilenceParams() *mediasample.SilenceParams {
	return &mediasample.SilenceParams{NoiseDB: headSilenceNoiseDB, MinSeconds: headSilenceMinSeconds, Stereo: true}
}

// Layout of a chromaprint+silence:v1 payload, little-endian:
//
//	header   point count uint32, silence count uint32
//	points   uint32 each, as chromaprint:raw:uint32le
//	silence  start ms uint32, end ms uint32, counted from the window start
//
// A silence still open when the window ended is stored ending at the window
// end; see storedSilences.

// silenceMillis is a silence boundary as the payload stores it: whole
// milliseconds from the window start.
func silenceMillis(seconds, windowStart float64) uint32 {
	return uint32(math.Round(math.Max(0, seconds-windowStart) * 1000))
}

// silenceSeconds is a stored silence boundary in media seconds.
func silenceSeconds(millis uint32, windowStart float64) float64 {
	return windowStart + float64(millis)/1000
}

// storedSilences returns silences as their row will read back: a silence
// ffmpeg left open at the end of the window ends there, and boundaries are
// whole milliseconds. Matching a fresh fingerprint on these keeps its result
// the same as matching the cached one later.
func storedSilences(silences []mediasample.Interval, windowStart, windowEnd float64) []mediasample.Interval {
	for i, silence := range silences {
		if silence.End <= 0 {
			silence.End = windowEnd
		}
		silences[i] = mediasample.Interval{
			Start: silenceSeconds(silenceMillis(silence.Start, windowStart), windowStart),
			End:   silenceSeconds(silenceMillis(silence.End, windowStart), windowStart),
		}
	}
	return silences
}

// encodeFingerprintSilences encodes points and silences found in a window
// that starts at windowStart (media seconds).
func encodeFingerprintSilences(points []uint32, silences []mediasample.Interval, windowStart float64) []byte {
	buf := make([]byte, 0, 8+4*len(points)+8*len(silences))
	// A window is at most hours long, so both counts stay far inside uint32.
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(points)))   //nolint:gosec // Bounded by the window length.
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(silences))) //nolint:gosec // Bounded by the window length.
	for _, point := range points {
		buf = binary.LittleEndian.AppendUint32(buf, point)
	}
	for _, silence := range silences {
		buf = binary.LittleEndian.AppendUint32(buf, silenceMillis(silence.Start, windowStart))
		buf = binary.LittleEndian.AppendUint32(buf, silenceMillis(silence.End, windowStart))
	}
	return buf
}

// decodeFingerprintSilences decodes a chromaprint+silence:v1 payload into
// points and silences in media seconds.
func decodeFingerprintSilences(payload []byte, windowStart float64) ([]uint32, []mediasample.Interval, error) {
	if len(payload) < 8 {
		return nil, nil, fmt.Errorf("fingerprint payload of %d bytes has no header", len(payload))
	}
	points := int(binary.LittleEndian.Uint32(payload))
	silences := int(binary.LittleEndian.Uint32(payload[4:]))
	if want := 8 + 4*points + 8*silences; points < 0 || silences < 0 || len(payload) != want {
		return nil, nil, fmt.Errorf("fingerprint payload has %d bytes, want %d", len(payload), want)
	}
	outPoints := make([]uint32, points)
	for i := range outPoints {
		outPoints[i] = binary.LittleEndian.Uint32(payload[8+4*i:])
	}
	outSilences := make([]mediasample.Interval, silences)
	base := 8 + 4*points
	for i := range outSilences {
		offset := base + 8*i
		outSilences[i] = mediasample.Interval{
			Start: silenceSeconds(binary.LittleEndian.Uint32(payload[offset:]), windowStart),
			End:   silenceSeconds(binary.LittleEndian.Uint32(payload[offset+4:]), windowStart),
		}
	}
	return outPoints, outSilences, nil
}

// Silence splitting and edge refinement bounds, tuned in replay against
// authored intro chapters.
const (
	// minRunNonSilentSeconds and minRunNonSilentFraction reject pair runs that
	// matched mostly digital silence.
	minRunNonSilentSeconds  = 4.5
	minRunNonSilentFraction = 0.35

	// pairSilenceSplitMinSeconds is the shortest silence that splits a pair
	// match. pairSilenceSplitMinKeep keeps the unsplit run if splitting would
	// leave no piece this long (preventing internal pauses in short intros from
	// dropping the match altogether).
	pairSilenceSplitMinSeconds = 0.45
	pairSilenceSplitMinKeep    = 10.0

	// nearZeroBumperWindowSeconds separates matches starting at the file head
	// (which often include a studio logo before the theme) from cold-open
	// intros, where early internal pauses belong to the theme.
	nearZeroBumperWindowSeconds = 5.0
	coldOpenProtectedHeadSec    = 15.0

	// Post-vote silence edge refinement bounds. Near-zero bumpers are already
	// split in pair matching; after a cold open, only a crisp transition
	// silence (0.8 to 3.0 s) ending within 8 s moves the start, so brief
	// musical breaths and long quiet fade-ins do not push starts late.
	coldStartSilenceReachSeconds = 8.0
	coldStartSilenceMinSeconds   = 0.8
	coldStartSilenceMaxSeconds   = 3.0
	endSilenceReachSeconds       = 15.0
	silenceRefineMinKeepSeconds  = 9.5

	// leadingSilenceSnapGuardSeconds prevents zero-start snapping when a
	// genuine opening silence precedes the detected start.
	leadingSilenceSnapGuardSeconds = 0.35
)

// nonSilentDuration returns how many seconds of [start, end] fall outside
// silences (which are sorted by start time).
func nonSilentDuration(start, end float64, silences []mediasample.Interval) float64 {
	if end <= start {
		return 0
	}
	silent := 0.0
	for _, s := range silences {
		if s.End <= start {
			continue
		}
		if s.Start >= end {
			break
		}
		lo := math.Max(start, s.Start)
		hi := math.Min(end, s.End)
		if hi > lo {
			silent += hi - lo
		}
	}
	return math.Max(0, (end-start)-silent)
}

// splitSegmentBySilences drops mostly-silent pair matches and splits runs that
// bridge a studio bumper or post-intro audio across a silence. A file without
// stored silences keeps its match unchanged.
func splitSegmentBySilences(seg Segment, silences []mediasample.Interval, minPairSeconds float64) []Segment {
	dur := seg.End - seg.Start
	if dur <= 0 {
		return nil
	}
	if len(silences) == 0 {
		return []Segment{seg}
	}
	ns := nonSilentDuration(seg.Start, seg.End, silences)
	if ns < minRunNonSilentSeconds || ns < minRunNonSilentFraction*dur {
		return nil
	}
	var clipped []mediasample.Interval
	for _, s := range silences {
		if s.End <= seg.Start {
			continue
		}
		if s.Start >= seg.End {
			break
		}
		lo := math.Max(seg.Start, s.Start)
		hi := math.Min(seg.End, s.End)
		if hi > lo {
			clipped = append(clipped, mediasample.Interval{Start: lo, End: hi})
		}
	}
	if len(clipped) == 0 {
		return []Segment{seg}
	}

	nearZero := seg.Start < nearZeroBumperWindowSeconds
	var pieces []Segment
	cur := seg.Start
	for _, s := range clipped {
		if s.End-s.Start < pairSilenceSplitMinSeconds {
			continue
		}
		if !nearZero && s.Start-seg.Start < coldOpenProtectedHeadSec && seg.End-s.End >= minPairSeconds {
			continue
		}
		if s.Start > cur {
			pieces = append(pieces, Segment{Start: cur, End: s.Start})
		}
		cur = s.End
	}
	if seg.End > cur {
		pieces = append(pieces, Segment{Start: cur, End: seg.End})
	}

	var kept []Segment
	for _, sp := range pieces {
		ls, le := sp.Start, sp.End
		for _, s := range clipped {
			if nearZero && s.Start <= ls+0.02 && s.End > ls && s.End < le {
				ls = s.End
			}
			if s.End >= le-0.02 && s.Start < le && s.Start > ls {
				le = s.Start
			}
		}
		if le-ls >= minPairSeconds {
			kept = append(kept, Segment{Start: ls, End: le})
		}
	}

	needKeep := math.Min(pairSilenceSplitMinKeep, dur)
	for _, p := range kept {
		if p.End-p.Start >= needKeep {
			return kept
		}
	}
	if dur >= minPairSeconds {
		return []Segment{seg}
	}
	return nil
}

// refineEdgesBySilence moves a consensus intro's edges off silences the file's
// own fingerprint pass recorded: after a cold open, a crisp transition silence
// ending within coldStartSilenceReachSeconds advances the start; at the end,
// the first silence beginning within endSilenceReachSeconds trims the tail,
// provided silenceRefineMinKeepSeconds remain.
func refineEdgesBySilence(segment Segment, silences []mediasample.Interval) Segment {
	if len(silences) == 0 {
		return segment
	}
	refined := segment
	if segment.Start >= nearZeroBumperWindowSeconds {
		for _, silence := range silences {
			if silence.End <= segment.Start || silence.End > segment.Start+coldStartSilenceReachSeconds {
				continue
			}
			sdur := silence.End - silence.Start
			if sdur < coldStartSilenceMinSeconds || sdur > coldStartSilenceMaxSeconds {
				continue
			}
			if segment.End-silence.End >= silenceRefineMinKeepSeconds {
				refined.Start = math.Max(refined.Start, silence.End)
			}
		}
	}
	for _, silence := range silences {
		if silence.Start >= segment.End || silence.Start < segment.End-endSilenceReachSeconds {
			continue
		}
		if silence.Start-refined.Start >= silenceRefineMinKeepSeconds {
			refined.End = silence.Start
			break
		}
	}
	return refined
}

// hasLeadingSilence reports whether silences contains a stretch of at least
// minDuration inside [0, start].
func hasLeadingSilence(silences []mediasample.Interval, start, minDuration float64) bool {
	for _, s := range silences {
		if s.Start >= start {
			break
		}
		if s.End <= 0 {
			continue
		}
		if math.Min(start, s.End)-math.Max(0, s.Start) >= minDuration {
			return true
		}
	}
	return false
}
