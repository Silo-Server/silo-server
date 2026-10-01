package handlers

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type watchMarkerPopulation struct {
	calls    []int
	err      error
	partial  bool
	deadline time.Time
}

func (p *watchMarkerPopulation) Populate(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	p.calls = append(p.calls, file.ID)
	p.deadline, _ = ctx.Deadline()
	if p.err != nil && !p.partial {
		return nil, false, p.err
	}
	copy := *file
	copy.MarkerSegments = []models.MarkerSegment{{Kind: "intro", StartSeconds: 10, EndSeconds: 20}, {Kind: "intro", StartSeconds: 40, EndSeconds: 50}}
	copy.IntroStart, copy.IntroEnd = new(10.0), new(20.0)
	return &copy, false, p.err
}

func TestMarkerReadKeepsPartialPopulationResultAndBoundsLookup(t *testing.T) {
	population := &watchMarkerPopulation{err: errors.New("another provider failed"), partial: true}
	before := time.Now()
	file := populateFileMarkers(t.Context(), population, &models.MediaFile{ID: 1})
	if len(file.MarkerSegments) != 2 {
		t.Fatalf("partial provider success was discarded: %+v", file.MarkerSegments)
	}
	if population.deadline.IsZero() || population.deadline.After(time.Now().Add(markerReadTimeout)) || !population.deadline.After(before) {
		t.Fatalf("lookup deadline = %v, want bounded marker read", population.deadline)
	}
}

func TestWatchMarkerPopulationUsesOnlyAccessibleSelectedVersion(t *testing.T) {
	for _, selected := range []int{0, 2, 99} {
		t.Run(strconv.Itoa(selected), func(t *testing.T) {
			population := &watchMarkerPopulation{}
			h := &ItemsHandler{MarkerPopulation: population, MarkerFileResolver: fakeMarkerFiles{byID: map[int]*models.MediaFile{1: {ID: 1}, 2: {ID: 2}, 99: {ID: 99}}}}
			detail := &catalog.WatchDetail{
				Versions:         []catalog.FileVersion{{FileID: 1}, {FileID: 2}},
				PlaybackVariants: []catalog.PlaybackVariant{{DefaultFileID: 2, Parts: []catalog.PlaybackVariantPart{{Versions: []catalog.FileVersion{{FileID: 2}}}}}},
			}
			h.populateWatchMarkers(t.Context(), detail, selected)
			if selected == 99 {
				if len(population.calls) != 0 {
					t.Fatal("inaccessible requested file triggered marker lookup")
				}
				return
			}
			if len(population.calls) != 1 || population.calls[0] != 2 {
				t.Fatalf("population calls = %v, want selected/default file 2 only", population.calls)
			}
			if len(detail.Versions[0].MarkerSegments) != 0 || len(detail.Versions[1].MarkerSegments) != 2 || len(detail.PlaybackVariants[0].Parts[0].Versions[0].MarkerSegments) != 2 {
				t.Fatal("populated ranges not projected onto matching versions")
			}
			if detail.Intro == nil || detail.Intro.End != 20 {
				t.Fatalf("legacy intro projection = %+v", detail.Intro)
			}
		})
	}
}

func TestMarkerReadPopulatesAfterAuthorizationAndPreservesExistingOnFailure(t *testing.T) {
	h, _, _ := markerServiceFixture()
	population := &watchMarkerPopulation{err: errors.New("provider unavailable")}
	h.MarkerPopulation = population
	h.Authorizer.ItemAccess = stubItemAccessChecker{err: catalog.ErrItemNotFound}
	if _, err := h.GetMarkers(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 5}); err == nil || len(population.calls) != 0 {
		t.Fatal("unauthorized marker read triggered population")
	}
	h.Authorizer.ItemAccess = stubItemAccessChecker{}
	view, err := h.GetMarkers(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 5})
	if err != nil || view.FileID != 5 || len(population.calls) != 1 {
		t.Fatalf("provider failure disrupted authorized read: view=%+v error=%v calls=%v", view, err, population.calls)
	}
}
