package trickplay

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// limitedPQ is an HDR10 file's video track recorded as limited range.
const limitedPQ = `[{"codec":"hevc","width":3840,"height":2160,"aspect_ratio":"16:9","bit_depth":10,
	"color_transfer":"smpte2084","color_primaries":"bt2020","color_space":"bt2020nc","color_range":"tv"}]`

// hookExtractor runs hook before its first extraction, between the claim
// and the publish.
type hookExtractor struct {
	*fakeExtractor
	hook func()
}

func (e *hookExtractor) Extract(ctx context.Context, job *Job, req mediasample.Request) (mediasample.Result, error) {
	if e.hook != nil {
		e.hook()
		e.hook = nil
	}
	return e.fakeExtractor.Extract(ctx, job, req)
}

// publishLimitedPQ queues an HDR10 file recorded as limited range and
// publishes its sheets through the service, running duringClaim (if any)
// while they are generated. It returns the service and the file.
func publishLimitedPQ(t *testing.T, f *fixture, duringClaim func(file int)) (*Service, int) {
	t.Helper()
	file := f.file(t, f.library(t, "movies", true), "tonemap-source")
	f.exec(t, `UPDATE media_files SET hdr = true, codec_video = 'hevc', video_tracks = $2::jsonb WHERE id = $1`, file, limitedPQ)
	extractor := &hookExtractor{fakeExtractor: &fakeExtractor{}}
	if duringClaim != nil {
		extractor.hook = func() { duringClaim(file) }
	}
	s := newService(f.repo, &fakeStore{}, fakeSettings{}, extractor, "server")
	s.logger = slog.New(slog.DiscardHandler)
	s.pool = f.pool
	reconcile(t, s)
	job, err := f.repo.ClaimFile(t.Context(), file, s.owner, leaseDuration)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	s.process(t.Context(), job)
	if row, _ := f.row(t, file); row.state != stateReady {
		t.Fatalf("sheets did not publish: %+v", row)
	}
	if tm := extractor.requests[0].Sheets.ToneMap; tm == nil || tm.Source != tonemap.SourcePQ {
		t.Fatalf("sheets tone mapped as %+v, want %s", tm, tonemap.SourcePQ)
	}
	return s, file
}

// reconcile runs one pass through the service, on its advisory lock's
// session.
func reconcile(t *testing.T, s *Service) {
	t.Helper()
	if _, ran, err := s.Reconcile(t.Context()); err != nil || !ran {
		t.Fatalf("reconcile: ran=%t error=%v", ran, err)
	}
}

// TestReconcileRequeuesSheetsWhoseToneMapSourceChangedDB covers a file whose
// stored color facts change without its bytes changing, as a rescan or a
// backfill can: an HDR10 file the catalog first records as limited range,
// then as full range, which playback converts as pq_full_range.
func TestReconcileRequeuesSheetsWhoseToneMapSourceChangedDB(t *testing.T) {
	toFullRange := `UPDATE media_files SET video_tracks = jsonb_set(video_tracks, '{0,color_range}', '"pc"') WHERE id = $1`
	unrelated := `UPDATE media_files SET video_tracks = jsonb_set(video_tracks, '{0,bitrate}', '20000000') WHERE id = $1`
	for _, tc := range []struct {
		name         string
		change       string
		duringClaim  bool
		wantRequeued bool
	}{
		{name: "range changes after publish", change: toFullRange, wantRequeued: true},
		{name: "range changes while generating", change: toFullRange, duringClaim: true, wantRequeued: true},
		{name: "unrelated track change", change: unrelated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			var during func(int)
			if tc.duringClaim {
				during = func(file int) { f.exec(t, tc.change, file) }
			}
			s, file := publishLimitedPQ(t, f, during)
			if !tc.duringClaim {
				reconcile(t, s)
				if row, _ := f.row(t, file); row.state != stateReady {
					t.Fatalf("unchanged file requeued: %+v", row)
				}
				f.exec(t, tc.change, file)
			}

			reconcile(t, s)
			row, _ := f.row(t, file)
			if requeued := row.state == statePending; requeued != tc.wantRequeued {
				t.Fatalf("requeued = %t, want %t: %+v", requeued, tc.wantRequeued, row)
			}
		})
	}
}

// TestReconcileAdoptsToneMapSourceOfOlderSheetsDB covers sheets published
// before their conversion was recorded: they keep serving rather than
// regenerating, and a later change to their facts requeues them. Those whose
// facts now call for full-range PQ, which older sheets may predate, are made
// again.
func TestReconcileAdoptsToneMapSourceOfOlderSheetsDB(t *testing.T) {
	toFullRange := `UPDATE media_files SET video_tracks = jsonb_set(video_tracks, '{0,color_range}', '"pc"') WHERE id = $1`
	t.Run("adopted", func(t *testing.T) {
		f := newFixture(t)
		s, file := publishLimitedPQ(t, f, nil)
		f.exec(t, `UPDATE media_file_trickplay SET tone_map_source = NULL WHERE media_file_id = $1`, file)

		reconcile(t, s)
		var source *string
		if err := f.pool.QueryRow(t.Context(), `SELECT tone_map_source FROM media_file_trickplay WHERE media_file_id = $1`, file).Scan(&source); err != nil {
			t.Fatal(err)
		}
		if row, _ := f.row(t, file); row.state != stateReady || source == nil || *source != string(tonemap.SourcePQ) {
			t.Fatalf("older sheets were not adopted: source=%v row=%+v", source, row)
		}

		f.exec(t, toFullRange, file)
		reconcile(t, s)
		if row, _ := f.row(t, file); row.state != statePending {
			t.Fatalf("adopted sheets were not requeued after their facts changed: %+v", row)
		}
	})
	t.Run("full range made again", func(t *testing.T) {
		f := newFixture(t)
		s, file := publishLimitedPQ(t, f, nil)
		f.exec(t, `UPDATE media_file_trickplay SET tone_map_source = NULL WHERE media_file_id = $1`, file)
		f.exec(t, toFullRange, file)

		reconcile(t, s)
		if row, _ := f.row(t, file); row.state != statePending {
			t.Fatalf("older sheets of a full-range file were not requeued: %+v", row)
		}
	})
}
