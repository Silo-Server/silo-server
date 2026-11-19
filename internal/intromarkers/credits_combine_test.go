package intromarkers

import (
	"fmt"
	"strconv"
	"strings"
)

var fixtureClasses = map[byte]keyframeClass{'c': keyframeContent, 'b': keyframeBlack, 'L': keyframeLettered, 'C': keyframeCard, 'm': keyframeMostlyBlack}

// parseKeyframes reads space-separated "<seconds><class>" tokens such as
// "2599.472L".
func parseKeyframes(spec string) ([]creditsKeyframe, error) {
	var out []creditsKeyframe
	for _, token := range strings.Fields(spec) {
		class, ok := fixtureClasses[token[len(token)-1]]
		if !ok {
			return nil, fmt.Errorf("keyframe %q has no class", token)
		}
		seconds, err := strconv.ParseFloat(token[:len(token)-1], 64)
		if err != nil {
			return nil, fmt.Errorf("keyframe %q: %w", token, err)
		}
		out = append(out, creditsKeyframe{Seconds: seconds, Class: class})
	}
	return out, nil
}

// creditsCase is one validated file from testdata/credits_cases.txt.
type creditsCase struct {
	name      string
	movie     bool
	candidate Candidate
	window    float64
	evidence  creditsEvidence
	want      *Segment
}
