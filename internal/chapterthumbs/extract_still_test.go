package chapterthumbs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Only a failed node reservation means no extraction can run; undecodable
// data means this file cannot until it changes; anything else may pass,
// including a node's failure response for one file.
func TestExtractStillClassifiesFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		reason string
		err    error
		want   error
	}{
		{"no node reserved", chapterThumbnailNodeUnavailableReason,
			wrapReason(chapterThumbnailNodeUnavailableReason, fmt.Errorf("%w for chapter thumbnail extraction", errNoTranscodeNode)), ErrExtractionUnavailable},
		{"capacity exhausted", chapterThumbnailNodeCapacityExhaustedReason,
			wrapReason(chapterThumbnailNodeCapacityExhaustedReason, fmt.Errorf("%w for chapter thumbnail extraction", errNoTranscodeNode)), ErrExtractionUnavailable},
		{"node response naming itself unavailable", chapterThumbnailNodeUnavailableReason, errors.New("remote 400"), nil},
		{"undecodable data", reasonDecodeInvalidData, errors.New("invalid data"), ErrUndecodableFile},
		{"probe failure", reasonFFmpegProbeFailed, errors.New("probe"), nil},
		{"timeout", reasonCPUTimeout, errors.New("timeout"), nil},
		{"tone mapping off", reasonToneMapUnsupported, errors.New("tonemap"), nil},
	} {
		s := &Service{extractFrameFunc: func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
			return nil, tt.reason, tt.err
		}}
		_, err := s.ExtractStill(t.Context(), &models.MediaFile{}, 1)
		if err == nil {
			t.Fatalf("%s: no error", tt.name)
		}
		unavailable, undecodable := errors.Is(err, ErrExtractionUnavailable), errors.Is(err, ErrUndecodableFile)
		if unavailable != errors.Is(tt.want, ErrExtractionUnavailable) || undecodable != errors.Is(tt.want, ErrUndecodableFile) {
			t.Errorf("%s: err = %v, want class %v", tt.name, err, tt.want)
		}
	}
}
