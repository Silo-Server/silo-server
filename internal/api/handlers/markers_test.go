package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

// markerTestHandler wires a markers handler over one stored file that the
// viewer may access.
func markerTestHandler(file *models.MediaFile, writer ManualMarkerWriter, contributor MarkerContributor) *MarkersHandler {
	file.ContentID = "item"
	files := fakeMarkerFiles{file: file}
	h := newTestMarkersHandler(files, writer, contributor, nil, nil, nil)
	h.Authorizer = &MediaFileAuthorizer{FileResolver: files, ItemAccess: stubItemAccessChecker{}}
	return h
}

func markerChanges(t *testing.T, body string) MarkerChanges {
	t.Helper()
	var changes MarkerChanges
	if err := json.Unmarshal([]byte(body), &changes); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return changes
}

// A manual save that keeps a provider's marker, both bounds within a second,
// or a clear that repeats a provider's answer that the file has none of the
// kind, stores the withheld algorithm so no contribution passes the
// provider's data on. In on_demand mode no replica knows which provider
// marker or answer a player was shown, so every save is withheld, unless the
// file already held the kind as a shared manual marker, which no provider
// marker replaces. A save that keeps an earlier withheld save stays withheld.
func TestSetMarkersWithholdsASaveThatKeepsAProviderMarker(t *testing.T) {
	plugin := func() *models.MediaFile {
		return &models.MediaFile{ID: 5, Duration: 1800, IntroStart: new(10.0), IntroEnd: new(70.0),
			IntroMarkersSource: new(models.MarkerSourcePlugin), IntroMarkersProvider: new("theintrodb")}
	}
	local := func(source, algorithm string) *models.MediaFile {
		file := plugin()
		file.IntroMarkersSource, file.IntroMarkersProvider, file.IntroMarkersAlgorithm = new(source), nil, new(algorithm)
		return file
	}
	unmarked := func() *models.MediaFile { return &models.MediaFile{ID: 5, Duration: 1800} }
	cleared := &models.MediaFile{ID: 5, Duration: 1800, IntroMarkersSource: new(models.MarkerSourceManual), IntroMarkersAlgorithm: new(markers.ManualAlgorithm)}
	twice := plugin()
	twice.MarkerSegments = []models.MarkerSegment{{Kind: models.MarkerSegmentIntro, StartSeconds: 10, EndSeconds: 30}, {Kind: models.MarkerSegmentIntro, StartSeconds: 100, EndSeconds: 130}}
	const kept = `{"intro":{"start":10.5,"end":70.9}}`
	for _, tc := range []struct {
		name       string
		stored     *models.MediaFile
		population MarkerPopulationService
		body       string
		withheld   bool
	}{
		{"kept within a second", plugin(), nil, kept, true},
		{"moved", plugin(), nil, `{"intro":{"start":11.5,"end":70}}`, false},
		{"kept a later occurrence", twice, nil, `{"intro":{"start":100.3,"end":130.3}}`, true},
		{"local marker", local(models.MarkerSourceScanner, "chapter:v1"), nil, kept, false},
		{"own correction", local(models.MarkerSourceManual, markers.ManualAlgorithm), nil, kept, false},
		{"kept an earlier withheld save", local(models.MarkerSourceManual, markers.ManualWithheldAlgorithm), nil, kept, true},
		{"on-demand, unmarked", unmarked(), onDemandMarkerPopulation{}, kept, true},
		{"on-demand, local marker", local(models.MarkerSourceScanner, "chapter:v1"), onDemandMarkerPopulation{}, kept, true},
		{"storage setting unreadable", unmarked(), onDemandMarkerPopulation{err: context.DeadlineExceeded}, kept, true},
		{"on-demand, own correction", local(models.MarkerSourceManual, markers.ManualAlgorithm), onDemandMarkerPopulation{}, kept, false},
		{"on-demand, earlier withheld save", local(models.MarkerSourceManual, markers.ManualWithheldAlgorithm), onDemandMarkerPopulation{}, kept, true},
		// An editor opened before the earlier save may show a provider's marker.
		{"on-demand, moved from an earlier withheld save", local(models.MarkerSourceManual, markers.ManualWithheldAlgorithm), onDemandMarkerPopulation{}, `{"intro":{"start":20,"end":80}}`, true},
		{"on-demand, manual clear", cleared, onDemandMarkerPopulation{}, kept, true},
		{"cleared a provider's absent answer", &models.MediaFile{ID: 5, Duration: 1800,
			IntroMarkersSource: new(models.MarkerSourcePlugin), IntroMarkersProvider: new("theintrodb")}, nil, `{"intro":null}`, true},
		{"cleared a provider marker", plugin(), nil, `{"intro":null}`, false},
		{"clear, on-demand", unmarked(), onDemandMarkerPopulation{}, `{"intro":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &fakeMarkerWriter{}
			h := markerTestHandler(tc.stored, writer, nil)
			h.MarkerPopulation = tc.population

			if _, err := h.SetMarkers(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 5}, markerChanges(t, tc.body)); err != nil {
				t.Fatalf("SetMarkers() error = %v", err)
			}
			if len(writer.upserts) > 1 {
				t.Fatalf("upserts = %d, want at most one", len(writer.upserts))
			}
			// A clear the update leaves alone is stored as ManualAlgorithm.
			algorithm := markers.ManualAlgorithm
			if len(writer.upserts) == 1 {
				algorithm = writer.upserts[0].MarkersAlgorithm
				if p := writer.upserts[0].IntroProvenance; p != nil {
					algorithm = p.Algorithm
				}
			}
			if want := map[bool]string{false: markers.ManualAlgorithm, true: markers.ManualWithheldAlgorithm}[tc.withheld]; algorithm != want {
				t.Fatalf("intro algorithm = %q, want %q", algorithm, want)
			}
		})
	}
}

// A manual save or clear starts a background contribution of exactly the
// changed segment. ContributeFile sends a clear only to providers that accept
// absent submissions.
func TestManualMarkerChangesAreContributed(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*MarkersHandler) error
		want markers.MarkerKind
	}{
		{"save", func(h *MarkersHandler) error {
			_, err := h.SetMarkers(t.Context(), catalog.AccessFilter{}, MarkerTarget{FileID: 5}, markerChanges(t, `{"intro":{"start":0,"end":60}}`))
			return err
		}, markers.MarkerKindIntro},
		{"clear", func(h *MarkersHandler) error {
			_, err := h.ClearMarker(t.Context(), catalog.AccessFilter{}, 5, "credits")
			return err
		}, markers.MarkerKindCredits},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := make(chan markers.ContributeOptions, 1)
			h := markerTestHandler(&models.MediaFile{ID: 5, Duration: 1800}, &fakeMarkerWriter{}, signalContributor{called: called})
			if err := tc.call(h); err != nil {
				t.Fatalf("marker change: %v", err)
			}
			select {
			case opts := <-called:
				if len(opts.Segments) != 1 || opts.Segments[0] != tc.want {
					t.Fatalf("contribute segments = %v, want [%v]", opts.Segments, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("no background contribution for the %s", tc.name)
			}
		})
	}
}
