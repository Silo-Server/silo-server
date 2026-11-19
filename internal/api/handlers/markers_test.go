package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type fakeMarkerFiles struct {
	file         *models.MediaFile
	byID         map[int]*models.MediaFile
	contentFiles []*models.MediaFile
	episodeFiles []*models.MediaFile
}

func (f fakeMarkerFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	if f.byID != nil {
		return f.byID[id], nil
	}
	return f.file, nil
}
func (f fakeMarkerFiles) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return f.contentFiles, nil
}
func (f fakeMarkerFiles) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return f.episodeFiles, nil
}

type fakeMarkerWriter struct {
	upserts []scanner.MarkerUpdate
	clears  [][]string
	audits  []scanner.MarkerAuditContext
}

func (f *fakeMarkerWriter) captureAudit(ctx context.Context) {
	if audit, ok := scanner.MarkerAuditContextFromContext(ctx); ok {
		f.audits = append(f.audits, audit)
	}
}

func (f *fakeMarkerWriter) UpsertMarkers(ctx context.Context, _ int, u scanner.MarkerUpdate) (bool, error) {
	f.captureAudit(ctx)
	f.upserts = append(f.upserts, u)
	return true, nil
}
func (f *fakeMarkerWriter) UpsertAndClearMarkers(ctx context.Context, _ int, u scanner.MarkerUpdate, segs []string) (bool, error) {
	f.captureAudit(ctx)
	if u.HasAnySegment() {
		f.upserts = append(f.upserts, u)
	}
	if len(segs) > 0 {
		f.clears = append(f.clears, segs)
	}
	return true, nil
}

// signalContributor reports every ContributeFile call on a channel so a test
// can wait for the detached background contribution kicked off by a save.
type signalContributor struct {
	called chan markers.ContributeOptions
}

func (s signalContributor) ContributeFile(_ context.Context, _ *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	s.called <- opts
	return nil, nil
}

// onDemandMarkerPopulation stands in for online lookups in on_demand mode,
// or for a failed read of that setting when err is set.
type onDemandMarkerPopulation struct{ err error }

func (p onDemandMarkerPopulation) Populate(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	return file, false, nil
}

func (p onDemandMarkerPopulation) ShowsStoredMarkers(context.Context) (bool, error) {
	return false, p.err
}
