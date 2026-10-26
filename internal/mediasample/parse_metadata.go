package mediasample

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// metadataFramePattern matches the line metadata=print starts each frame
// with: "frame:12   pts:1550   pts_time:1.55".
var metadataFramePattern = regexp.MustCompile(`^frame:\s*(\d+)\s+pts:\s*\S+\s+pts_time:\s*(\S+)`)

// metadataFrame is one frame metadata=print logged.
type metadataFrame struct {
	// Index is the filter's frame counter, which starts at zero.
	Index int64
	// PTSTime is the frame's time relative to the sampled span.
	PTSTime float64
	// Values holds the numeric lavfi.* entries, by full key.
	Values map[string]float64
	// Sample is the "sample" entry, which a sampled input list attaches to
	// each packet to name the media time it was taken for.
	Sample    float64
	HasSample bool
}

// metadataParser reads the log of one metadata=print instance: a frame line
// followed by one "key=value" line per entry. Lines of other filter
// instances, and entries before the first frame, are ignored.
type metadataParser struct {
	context string
	frames  []metadataFrame
	current *metadataFrame
}

func newMetadataParser(instance int) *metadataParser {
	return &metadataParser{context: fmt.Sprintf("[Parsed_metadata_%d @ ", instance)}
}

func (p *metadataParser) line(line string) {
	message, ok := filterMessage(line, p.context)
	if !ok {
		return
	}
	if match := metadataFramePattern.FindStringSubmatch(message); match != nil {
		p.current = nil
		index, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return
		}
		// A frame without a usable time (pts_time:NOPTS) cannot be placed,
		// so its entries are dropped with it.
		seconds, err := strconv.ParseFloat(match[2], 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return
		}
		p.frames = append(p.frames, metadataFrame{Index: index, PTSTime: seconds, Values: map[string]float64{}})
		p.current = &p.frames[len(p.frames)-1]
		return
	}
	if p.current == nil {
		return
	}
	key, raw, ok := strings.Cut(strings.TrimSpace(message), "=")
	if !ok {
		return
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	switch {
	case key == "sample":
		p.current.Sample, p.current.HasSample = value, true
	case strings.HasPrefix(key, "lavfi."):
		p.current.Values[key] = value
	}
}

// filterMessage returns what follows a filter instance's log context
// ("[Parsed_metadata_7 @ 0x5a…] "), or false when the line is not from that
// instance.
func filterMessage(line, context string) (string, bool) {
	start := strings.Index(line, context)
	if start < 0 {
		return "", false
	}
	rest := line[start+len(context):]
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return "", false
	}
	return strings.TrimPrefix(rest[end+1:], " "), true
}
