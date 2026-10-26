package mediasample

import (
	"regexp"
	"strconv"
	"strings"
)

// blackframePattern matches a blackframe report:
// "[Parsed_blackframe_3 @ 0x…] frame:12 pblack:97 pts:1550 t:1.550000 …".
var blackframePattern = regexp.MustCompile(`\[Parsed_blackframe_(\d+) @ [^\]]*\] frame:\s*(\d+) pblack:\s*(\d+)`)

// blackframeParser collects the reports of several blackframe instances by
// frame index. Each instance holds one slot, in the order the instances were
// given; reports from other instances are ignored.
type blackframeParser struct {
	slots  map[int]int
	frames map[int64][]int16
}

func newBlackframeParser(instances []int) *blackframeParser {
	slots := make(map[int]int, len(instances))
	for slot, instance := range instances {
		slots[instance] = slot
	}
	return &blackframeParser{slots: slots, frames: map[int64][]int16{}}
}

func (p *blackframeParser) line(line string) {
	if len(p.slots) == 0 || !strings.Contains(line, "[Parsed_blackframe_") {
		return
	}
	match := blackframePattern.FindStringSubmatch(line)
	if match == nil {
		return
	}
	instance, err := strconv.Atoi(match[1])
	if err != nil {
		return
	}
	slot, ok := p.slots[instance]
	if !ok {
		return
	}
	index, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil {
		return
	}
	pblack, err := strconv.Atoi(match[3])
	if err != nil || pblack < 0 || pblack > 100 {
		return
	}
	values := p.frames[index]
	if values == nil {
		values = make([]int16, len(p.slots))
		for i := range values {
			values[i] = -1
		}
		p.frames[index] = values
	}
	values[slot] = int16(pblack)
}

// values returns a frame's percentages in slot order, or false unless every
// instance reported the frame. With no instances every frame is complete.
func (p *blackframeParser) values(index int64) ([]uint8, bool) {
	if len(p.slots) == 0 {
		return nil, true
	}
	values, ok := p.frames[index]
	if !ok {
		return nil, false
	}
	out := make([]uint8, len(values))
	for i, value := range values {
		if value < 0 {
			return nil, false
		}
		out[i] = uint8(value)
	}
	return out, true
}
