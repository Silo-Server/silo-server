package downloads

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

func TestManifestMarkerPopulationIsSelectedAndSkipsBatches(t *testing.T) {
	file := &models.MediaFile{ID: 99, IntroStart: new(0.0), IntroEnd: new(5.0)}
	detail := &catalog.ItemDetail{Versions: []catalog.FileVersion{{FileID: 99}, {FileID: 100}}}
	population := &manifestMarkerPopulation{}
	b := NewManifestBuilder(fakeManifestSource{detail: detail}, nil, fakeFileResolver{file: file}, nil)
	b.MarkerPopulation = population
	dl := &Download{ID: "entry", ContentID: "item", MediaFileID: 99}
	m, err := b.Build(t.Context(), dl, catalog.AccessFilter{})
	if err != nil || population.calls != 1 || len(m.MarkerSegments) != 2 || m.Intro.End != 20 {
		t.Fatalf("selected manifest lost partial marker success: manifest=%+v calls=%d error=%v", m, population.calls, err)
	}
	m, err = b.build(t.Context(), dl, catalog.AccessFilter{}, map[string]*catalog.ItemDetail{}, false)
	if err != nil || population.calls != 1 || len(m.MarkerSegments) != 1 || m.MarkerSegments[0].EndSeconds != 5 {
		t.Fatalf("batch fetched providers instead of using stored markers: manifest=%+v calls=%d error=%v", m, population.calls, err)
	}
	dl.MediaFileID = 777
	if _, err := b.Build(t.Context(), dl, catalog.AccessFilter{}); err != nil || population.calls != 1 {
		t.Fatal("file absent from access-filtered detail triggered population")
	}
	b.detail = fakeManifestSource{err: catalog.ErrItemNotFound}
	if _, err := b.Build(t.Context(), dl, catalog.AccessFilter{}); !errors.Is(err, catalog.ErrItemNotFound) || population.calls != 1 {
		t.Fatal("denied manifest triggered population")
	}
}
