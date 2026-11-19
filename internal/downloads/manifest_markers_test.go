package downloads

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/models"
)

type manifestMarkerPopulation struct{ calls int }

func (p *manifestMarkerPopulation) Populate(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	p.calls++
	copy := *file
	copy.MarkerSegments = []models.MarkerSegment{{Kind: "intro", StartSeconds: 10, EndSeconds: 20}, {Kind: "intro", StartSeconds: 40, EndSeconds: 50}}
	copy.IntroStart, copy.IntroEnd = new(10.0), new(20.0)
	return &copy, false, errors.New("other provider failed")
}
