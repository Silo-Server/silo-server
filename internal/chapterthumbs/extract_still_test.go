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

// A file whose every video track is over the pixel limit is refused before
// ffmpeg starts; one track under it is enough to extract.
func TestExtractStillRefusesOversizedFrames(t *testing.T) {
	for _, tt := range []struct {
		name   string
		tracks []models.VideoTrack
		want   error
	}{
		{"every track over the limit", []models.VideoTrack{{Width: 16000, Height: 16000}}, ErrUndecodableFile},
		{"one track under the limit", []models.VideoTrack{{Width: 3840, Height: 2160}, {Width: 16000, Height: 16000}}, nil},
	} {
		calls := 0
		s := &Service{extractFrameFunc: func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
			calls++
			return []byte("frame"), "", nil
		}}
		_, err := s.ExtractStill(t.Context(), &models.MediaFile{VideoTracks: tt.tracks}, 1)
		if tt.want != nil && (!errors.Is(err, tt.want) || calls != 0) {
			t.Errorf("%s: err = %v after %d extractions, want %v before any", tt.name, err, calls, tt.want)
		}
		if tt.want == nil && (err != nil || calls != 1) {
			t.Errorf("%s: err = %v after %d extractions, want one frame", tt.name, err, calls)
		}
	}
}
