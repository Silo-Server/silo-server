package requests

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

func TestDownloadPhasePrecedence(t *testing.T) {
	// Highest first; an unknown phase ranks as downloading.
	order := []DownloadPhase{
		DownloadPhaseImportBlocked, DownloadPhaseStalled, DownloadPhaseDownloading,
		DownloadPhaseImporting, DownloadPhasePaused, DownloadPhaseQueued,
	}
	for i := 1; i < len(order); i++ {
		if downloadPhaseRank(order[i-1]) <= downloadPhaseRank(order[i]) {
			t.Fatalf("%s must outrank %s", order[i-1], order[i])
		}
	}
	if downloadPhaseRank("seeding") != downloadPhaseRank(DownloadPhaseDownloading) {
		t.Fatal("an unknown phase must rank as downloading")
	}
}

func TestRequestDownloadAggregatesLiveTargets(t *testing.T) {
	early := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	older, newer := early.Add(-2*time.Minute), early.Add(-time.Minute)
	req := Request{Targets: []Target{
		{Quality: Quality1080p, Status: StatusDownloading, Download: &DownloadProgress{
			Phase: DownloadPhaseDownloading, BytesTotal: 4000, BytesLeft: 1000, EstimatedCompletion: &early, Downloads: 1, UpdatedAt: newer,
		}},
		{Quality: Quality2160p, Status: StatusQueued, Download: &DownloadProgress{
			Phase: DownloadPhaseStalled, BytesTotal: 6000, BytesLeft: 6000, EstimatedCompletion: &late, Downloads: 2, UpdatedAt: older,
		}},
	}}
	got := req.Download()
	if got == nil {
		t.Fatal("Download() = nil, want the aggregate")
	}
	if got.Phase != DownloadPhaseStalled || got.BytesTotal != 10000 || got.BytesLeft != 7000 || got.Downloads != 3 {
		t.Fatalf("aggregate = %+v, want stalled, 10000/7000 bytes, 3 downloads", got)
	}
	if got.EstimatedCompletion == nil || !got.EstimatedCompletion.Equal(late) || !got.UpdatedAt.Equal(older) {
		t.Fatalf("aggregate = %+v, want the latest estimate and the oldest report", got)
	}
	// The aggregate is a copy: the targets keep their own figures.
	if req.Targets[0].Download.BytesTotal != 4000 || req.Targets[0].Download.Phase != DownloadPhaseDownloading {
		t.Fatalf("first target changed to %+v", req.Targets[0].Download)
	}
}

func TestRequestDownloadPhaseByPrecedence(t *testing.T) {
	for _, tc := range []struct {
		phases []DownloadPhase
		want   DownloadPhase
	}{
		{[]DownloadPhase{DownloadPhaseQueued, DownloadPhaseImportBlocked}, DownloadPhaseImportBlocked},
		{[]DownloadPhase{DownloadPhaseDownloading, DownloadPhaseStalled}, DownloadPhaseStalled},
		{[]DownloadPhase{DownloadPhaseImporting, DownloadPhaseDownloading}, DownloadPhaseDownloading},
		{[]DownloadPhase{DownloadPhasePaused, DownloadPhaseImporting}, DownloadPhaseImporting},
		{[]DownloadPhase{DownloadPhaseQueued, DownloadPhasePaused}, DownloadPhasePaused},
		{[]DownloadPhase{DownloadPhaseQueued, DownloadPhaseQueued}, DownloadPhaseQueued},
	} {
		req := Request{Targets: []Target{
			{Quality: Quality1080p, Status: StatusDownloading, Download: &DownloadProgress{Phase: tc.phases[0], BytesTotal: 1}},
			{Quality: Quality2160p, Status: StatusDownloading, Download: &DownloadProgress{Phase: tc.phases[1], BytesTotal: 1}},
		}}
		if got := req.Download(); got == nil || got.Phase != tc.want {
			t.Errorf("phases %v aggregate to %+v, want %s", tc.phases, got, tc.want)
		}
	}
}

func TestRequestDownloadWithAnUnknownSizeHasNoTotal(t *testing.T) {
	req := Request{Targets: []Target{
		{Quality: Quality1080p, Status: StatusDownloading, Download: &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 4000, BytesLeft: 1000, Downloads: 1}},
		{Quality: Quality2160p, Status: StatusDownloading, Download: &DownloadProgress{Phase: DownloadPhaseQueued, Downloads: 1}},
	}}
	got := req.Download()
	if got == nil || got.BytesTotal != 0 || got.BytesLeft != 0 || got.Downloads != 2 {
		t.Fatalf("aggregate = %+v, want no total while one size is unknown", got)
	}
}

// A live target that reports no progress yet (a 4K copy still waiting for a
// release, say) leaves the request's size unknown, so the request shows no
// percentage for its 1080p copy alone.
func TestRequestDownloadWithASilentLiveTargetHasNoTotal(t *testing.T) {
	req := Request{Targets: []Target{
		{Quality: Quality1080p, Status: StatusDownloading, Download: &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 4000, BytesLeft: 400, Downloads: 1}},
		{Quality: Quality2160p, Status: StatusQueued},
	}}
	got := req.Download()
	if got == nil || got.Phase != DownloadPhaseDownloading || got.BytesTotal != 0 || got.BytesLeft != 0 || got.Downloads != 1 {
		t.Fatalf("aggregate = %+v, want downloading with no total while the 4K copy reports nothing", got)
	}
	// A finished target is not live and does not hide the total.
	req.Targets[1].Status = StatusCompleted
	if got := req.Download(); got == nil || got.BytesTotal != 4000 || got.BytesLeft != 400 {
		t.Fatalf("aggregate = %+v, want the 1080p figures once the 4K copy is done", got)
	}
}

func TestRequestDownloadIgnoresFinishedTargetsAndTargetsWithoutProgress(t *testing.T) {
	stale := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 100}
	req := Request{Targets: []Target{
		{Quality: Quality1080p, Status: StatusCompleted, Download: stale},
		{Quality: Quality2160p, Status: StatusDownloading},
	}}
	if got := req.Download(); got != nil {
		t.Fatalf("Download() = %+v, want nil", got)
	}
	req.Targets = append(req.Targets, Target{Status: StatusFailed, Download: stale})
	if got := req.Download(); got != nil {
		t.Fatalf("Download() = %+v, want nil", got)
	}
}

func TestDownloadProgressFromProto(t *testing.T) {
	eta := time.Date(2026, 9, 28, 13, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		in   *pluginv1.DownloadProgress
		want *DownloadProgress
	}{
		{name: "unset", in: nil, want: nil},
		{name: "empty", in: &pluginv1.DownloadProgress{}, want: nil},
		{name: "no phase and no size", in: &pluginv1.DownloadProgress{Downloads: 1}, want: nil},
		{
			name: "known phase",
			in:   &pluginv1.DownloadProgress{Phase: "import_blocked", BytesTotal: 100, BytesLeft: 0, Downloads: 1, EstimatedCompletion: timestamppb.New(eta)},
			want: &DownloadProgress{Phase: DownloadPhaseImportBlocked, BytesTotal: 100, Downloads: 1, EstimatedCompletion: &eta},
		},
		{
			name: "empty phase with a size",
			in:   &pluginv1.DownloadProgress{BytesTotal: 100, BytesLeft: 40, Downloads: 1},
			want: &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 100, BytesLeft: 40, Downloads: 1},
		},
		{
			name: "unknown phase",
			in:   &pluginv1.DownloadProgress{Phase: "seeding", Downloads: 2},
			want: &DownloadProgress{Phase: DownloadPhaseDownloading, Downloads: 2},
		},
		{
			name: "queued without a size",
			in:   &pluginv1.DownloadProgress{Phase: "queued", Downloads: 1},
			want: &DownloadProgress{Phase: DownloadPhaseQueued, Downloads: 1},
		},
		{
			name: "clamped",
			in:   &pluginv1.DownloadProgress{Phase: "downloading", BytesTotal: 100, BytesLeft: 250, Downloads: -1},
			want: &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 100, BytesLeft: 100},
		},
		{
			name: "negative",
			in:   &pluginv1.DownloadProgress{Phase: "paused", BytesTotal: -5, BytesLeft: -5},
			want: &DownloadProgress{Phase: DownloadPhasePaused},
		},
		{
			name: "invalid estimate",
			in:   &pluginv1.DownloadProgress{Phase: "downloading", BytesTotal: 10, EstimatedCompletion: &timestamppb.Timestamp{Nanos: -1}},
			want: &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 10},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := downloadProgressFromProto(tc.in)
			if !sameProgress(got, tc.want) {
				t.Fatalf("downloadProgressFromProto() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPluginRouterProviderCheckStatusCarriesProgress(t *testing.T) {
	eta := time.Date(2026, 9, 28, 13, 0, 0, 0, time.UTC)
	fc := &fakeRouterClient{statuses: []*pluginv1.TargetStatus{
		{Quality: "1080p", ConnectionId: "c1", Status: "downloading", Progress: &pluginv1.DownloadProgress{
			Phase: "downloading", BytesTotal: 2000, BytesLeft: 500, Downloads: 1, EstimatedCompletion: timestamppb.New(eta),
		}},
		{Quality: "2160p", ConnectionId: "c1", Status: "queued"},
	}}
	out, err := NewPluginRouterProvider(fakeRouterResolver{c: fc}).CheckStatus(context.Background(), 1, "arr",
		Request{MediaType: MediaTypeMovie, TMDBID: 42}, []RouterTargetRef{{Quality: Quality1080p, ConnectionID: "c1"}}, nil)
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	want := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 2000, BytesLeft: 500, Downloads: 1, EstimatedCompletion: &eta}
	if len(out) != 2 || !sameProgress(out[0].Progress, want) || out[1].Progress != nil {
		t.Fatalf("statuses = %+v, want progress on the first only", out)
	}
}

// sameProgress compares progress reports, ignoring UpdatedAt, which the
// store stamps.
func sameProgress(a, b *DownloadProgress) bool {
	if a == nil || b == nil {
		return a == b
	}
	if (a.EstimatedCompletion == nil) != (b.EstimatedCompletion == nil) ||
		(a.EstimatedCompletion != nil && !a.EstimatedCompletion.Equal(*b.EstimatedCompletion)) {
		return false
	}
	return a.Phase == b.Phase && a.BytesTotal == b.BytesTotal && a.BytesLeft == b.BytesLeft && a.Downloads == b.Downloads
}

// seedDownloadTarget stores an active request with one target on connection.
func seedDownloadTarget(t *testing.T, store *fakeStore, requestID, connection string, quality Quality, status Status, download *DownloadProgress) Target {
	t.Helper()
	if store.requests[requestID] == nil {
		store.requests[requestID] = &Request{ID: requestID, MediaType: MediaTypeMovie, TMDBID: 550, Status: status, Outcome: OutcomeActive}
	}
	target, err := store.CreateTarget(context.Background(), Target{
		RequestID: requestID, IntegrationID: connection, Quality: quality, Status: status, ExternalID: "123", Download: download,
	})
	if err != nil {
		t.Fatalf("seed target: %v", err)
	}
	return target
}

func onlyTarget(t *testing.T, store *fakeStore, requestID string) Target {
	t.Helper()
	targets, _ := store.ListTargets(context.Background(), requestID)
	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want one", targets)
	}
	return targets[0]
}

func TestApplyTargetStatusesWritesAndClearsProgress(t *testing.T) {
	progress := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 250, Downloads: 1}
	earlier := &DownloadProgress{Phase: DownloadPhaseQueued, BytesTotal: 1000, BytesLeft: 1000, Downloads: 1}
	for _, tc := range []struct {
		name       string
		status     Status
		had        *DownloadProgress
		reported   RouterTargetStatus
		wantStatus Status
		wantChange reconcileChange
		wantWrites int
		want       *DownloadProgress
	}{
		{
			name: "progress without a status change", status: StatusDownloading,
			reported:   RouterTargetStatus{Status: StatusDownloading, Progress: progress},
			wantStatus: StatusDownloading, wantChange: reconcileUnchanged, wantWrites: 1, want: progress,
		},
		{
			name: "progress on the move to downloading", status: StatusQueued, had: earlier,
			reported:   RouterTargetStatus{Status: StatusDownloading, Progress: progress},
			wantStatus: StatusDownloading, wantChange: reconcileDownloading, wantWrites: 1, want: progress,
		},
		{
			name: "completion clears", status: StatusDownloading, had: earlier,
			reported:   RouterTargetStatus{Status: StatusCompleted, Progress: progress},
			wantStatus: StatusCompleted, wantChange: reconcileCompleted,
		},
		{
			name: "failure clears", status: StatusDownloading, had: earlier,
			reported:   RouterTargetStatus{Status: StatusFailed, Message: "no release"},
			wantStatus: StatusFailed, wantChange: reconcileFailed,
		},
		{
			name: "no progress clears what was reported", status: StatusDownloading, had: earlier,
			reported:   RouterTargetStatus{Status: StatusDownloading},
			wantStatus: StatusDownloading, wantChange: reconcileUnchanged, wantWrites: 1,
		},
		{
			name: "an idle target is not written", status: StatusQueued,
			reported:   RouterTargetStatus{Status: StatusQueued},
			wantStatus: StatusQueued, wantChange: reconcileUnchanged,
		},
		{
			name: "no status keeps the status and writes progress", status: StatusQueued,
			reported:   RouterTargetStatus{Progress: progress},
			wantStatus: StatusQueued, wantChange: reconcileUnchanged, wantWrites: 1, want: progress,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			target := seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, tc.status, tc.had)
			service := newTestService(store)
			reported := tc.reported
			reported.Quality, reported.ConnectionID = Quality1080p, "router-1"

			targets, _ := store.ListTargets(context.Background(), "req-1")
			change, err := service.applyTargetStatuses(context.Background(), targets, []RouterTargetStatus{
				reported,
				// A status for a target the request does not have is ignored.
				{Quality: Quality2160p, ConnectionID: "router-1", Status: StatusCompleted, Progress: progress},
			})
			if err != nil {
				t.Fatalf("applyTargetStatuses: %v", err)
			}
			got := onlyTarget(t, store, "req-1")
			if change != tc.wantChange || got.Status != tc.wantStatus {
				t.Fatalf("change = %s, status = %s; want %s, %s", change, got.Status, tc.wantChange, tc.wantStatus)
			}
			if len(store.downloadWrites) != tc.wantWrites {
				t.Fatalf("progress writes = %+v, want %d", store.downloadWrites, tc.wantWrites)
			}
			if tc.wantWrites > 0 && store.downloadWrites[0].targetID != target.ID {
				t.Fatalf("wrote target %d, want %d", store.downloadWrites[0].targetID, target.ID)
			}
			if !sameProgress(got.Download, tc.want) {
				t.Fatalf("stored progress = %+v, want %+v", got.Download, tc.want)
			}
		})
	}
}

func TestApplyTargetStatusesReportsAProgressWriteFailure(t *testing.T) {
	store := newFakeStore()
	seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading, nil)
	store.downloadErr = errors.New("database unavailable")
	targets, _ := store.ListTargets(context.Background(), "req-1")
	_, err := newTestService(store).applyTargetStatuses(context.Background(), targets, []RouterTargetStatus{{
		Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading,
		Progress: &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 10},
	}})
	if !errors.Is(err, store.downloadErr) {
		t.Fatalf("applyTargetStatuses err = %v, want the store's", err)
	}
}

// The reconcile pass records progress too, for queued targets and for a
// download's first report, which puts the target on the download refresh pass.
func TestReconcileRequestsRecordsDownloadProgress(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusQueued, nil)
	store.candidates = []*Request{store.requests["req-1"]}
	progress := &DownloadProgress{Phase: DownloadPhaseQueued, BytesTotal: 800, BytesLeft: 800, Downloads: 1}
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{progressCapable: map[int]bool{1: true}, statuses: []RouterTargetStatus{{
		Quality: Quality1080p, ConnectionID: "router-1", Status: StatusQueued, Progress: progress,
	}}})

	if _, err := service.ReconcileRequests(context.Background(), 100); err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	if got := onlyTarget(t, store, "req-1"); got.Status != StatusQueued || !sameProgress(got.Download, progress) {
		t.Fatalf("target = %+v, want queued with the reported progress", got)
	}
	if len(store.statusUpdates) != 0 {
		t.Fatalf("status updates = %v, want none for an unchanged status", store.statusUpdates)
	}
}

// Progress counts only from a plugin that declares reports_download_progress.
// The download refresh pass clears any other plugin's, so were reconcile to
// record it, it would come and go between the passes.
func TestReconcileRequestsIgnoresProgressFromUndeclaredPlugins(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	stale := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 800, BytesLeft: 100, Downloads: 1}
	seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading, stale)
	store.candidates = []*Request{store.requests["req-1"]}
	service := newTestService(store)
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{{
		Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading,
		Progress: &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 800, BytesLeft: 400, Downloads: 1},
	}}}
	service.SetRouterProvider(router)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil || result.Errors != 0 {
		t.Fatalf("ReconcileRequests = %+v, %v", result, err)
	}
	if got := onlyTarget(t, store, "req-1"); got.Status != StatusDownloading || got.Download != nil {
		t.Fatalf("target = %+v, want downloading with its stale progress cleared", got)
	}

	// When the declaration cannot be read, the report stands and the pass
	// reports the error.
	router.featuresErr = errors.New("capability metadata unavailable")
	result, err = service.ReconcileRequests(context.Background(), 100)
	if err != nil || result.Errors != 1 {
		t.Fatalf("ReconcileRequests = %+v, %v; want one error", result, err)
	}
	if got := onlyTarget(t, store, "req-1"); got.Download == nil || got.Download.BytesLeft != 400 {
		t.Fatalf("target = %+v, want the reported progress kept", got)
	}
}

// A server whose own state moves while the target's status does not (an
// import that stalls) has its raw status recorded beside the new progress,
// with no status write and so no history entry. An unchanged raw status is
// not written again.
func TestReconcileRequestsKeepsExternalStatusInStep(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	blocked := &DownloadProgress{Phase: DownloadPhaseImportBlocked, BytesTotal: 800, BytesLeft: 0, Downloads: 1}
	target := seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading, blocked)
	store.targets["req-1"][0].ExternalStatus = "completed/importBlocked"
	store.candidates = []*Request{store.requests["req-1"]}
	service := newTestService(store)
	stalled := &DownloadProgress{Phase: DownloadPhaseStalled, BytesTotal: 800, BytesLeft: 480, Downloads: 1}
	service.SetRouterProvider(&fakeRouterProvider{progressCapable: map[int]bool{1: true}, statuses: []RouterTargetStatus{{
		Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading, ExternalStatus: "warning/downloading", Progress: stalled,
	}}})

	for pass := 1; pass <= 2; pass++ {
		if _, err := service.ReconcileRequests(context.Background(), 100); err != nil {
			t.Fatalf("pass %d: ReconcileRequests: %v", pass, err)
		}
		got := onlyTarget(t, store, "req-1")
		if got.Status != StatusDownloading || got.ExternalStatus != "warning/downloading" || !sameProgress(got.Download, stalled) {
			t.Fatalf("pass %d: target = %+v, want downloading, stalled, with the new raw status", pass, got)
		}
		if len(store.statusUpdates) != 0 {
			t.Fatalf("pass %d: status updates = %v, want none for an unchanged status", pass, store.statusUpdates)
		}
		if want := []int64{target.ID}; !slices.Equal(store.externalStatusWrites, want) {
			t.Fatalf("pass %d: external status writes = %v, want %v, once", pass, store.externalStatusWrites, want)
		}
	}
}

// downloadRefreshService serves two download servers: router-1 on a plugin
// that reports progress, router-2 on one that does not.
func downloadRefreshService(store *fakeStore) (*Service, *fakeRouterProvider) {
	store.integrations = []Integration{routerInstOn("router-1", 1), routerInstOn("router-2", 2)}
	router := &fakeRouterProvider{progressCapable: map[int]bool{1: true}}
	service := newTestService(store)
	service.SetRouterProvider(router)
	return service, router
}

func TestRefreshDownloadsPollsOnlyDownloadingTargetsOfProgressPlugins(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	earlier := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1}
	// req-mixed: 1080p downloading on the reporting plugin, 4K downloading
	// on the other. req-queued waits in the queue on the reporting plugin.
	// req-legacy downloads only through the other plugin.
	seedDownloadTarget(t, store, "req-mixed", "router-1", Quality1080p, StatusDownloading, earlier)
	seedDownloadTarget(t, store, "req-mixed", "router-2", Quality2160p, StatusDownloading, nil)
	seedDownloadTarget(t, store, "req-queued", "router-1", Quality1080p, StatusQueued, earlier)
	seedDownloadTarget(t, store, "req-legacy", "router-2", Quality1080p, StatusDownloading, nil)
	progress := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 400, Downloads: 1}
	router.statuses = []RouterTargetStatus{
		{Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading, Progress: progress},
		{Quality: Quality2160p, ConnectionID: "router-2", Status: StatusCompleted},
	}
	presence := service.presence.(*fakePresence)

	result, err := service.RefreshDownloads(context.Background(), 200, 0)
	if err != nil {
		t.Fatalf("RefreshDownloads: %v", err)
	}
	if result != (DownloadRefreshResult{Checked: 1}) {
		t.Fatalf("result = %+v, want one request checked", result)
	}
	if len(router.statusLog) != 1 || router.statusLog[0].installationID != 1 ||
		len(router.statusLog[0].refs) != 1 || router.statusLog[0].refs[0].Quality != Quality1080p {
		t.Fatalf("status calls = %+v, want one call to installation 1 for the 1080p target", router.statusLog)
	}
	targets, _ := store.ListTargets(context.Background(), "req-mixed")
	for _, target := range targets {
		switch target.Quality {
		case Quality1080p:
			if !sameProgress(target.Download, progress) {
				t.Fatalf("1080p progress = %+v, want %+v", target.Download, progress)
			}
		case Quality2160p:
			// Not polled, so the completion reported for it was not applied.
			if target.Status != StatusDownloading || target.Download != nil {
				t.Fatalf("4K target = %+v, want it untouched", target)
			}
		}
	}
	if len(presence.got) != 0 || router.fulfillCalls != 0 {
		t.Fatalf("presence lookups = %d, submissions = %d; want neither", len(presence.got), router.fulfillCalls)
	}
}

func TestRefreshDownloadsAppliesStatusTransitions(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	// Were the pass to run the notification step, req-1 would be notified:
	// it is in the library and not notified yet.
	notifier := &fakeNotifier{}
	service.SetFulfillmentNotifier(notifier)
	service.presence.(*fakePresence).available = map[MediaType]map[int]bool{MediaTypeMovie: {550: true}}
	store.unnotified = []string{"req-1"}
	seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseImporting, BytesTotal: 10, Downloads: 1})
	router.statuses = []RouterTargetStatus{{Quality: Quality1080p, ConnectionID: "router-1", Status: StatusCompleted, ExternalStatus: "imported"}}

	result, err := service.RefreshDownloads(context.Background(), 200, 0)
	if err != nil {
		t.Fatalf("RefreshDownloads: %v", err)
	}
	if result != (DownloadRefreshResult{Checked: 1, Updated: 1}) {
		t.Fatalf("result = %+v, want one request checked and updated", result)
	}
	if got := onlyTarget(t, store, "req-1"); got.Status != StatusCompleted || got.Download != nil {
		t.Fatalf("target = %+v, want completed without progress", got)
	}
	if store.requests["req-1"].Status != StatusCompleted {
		t.Fatalf("request status = %s, want completed", store.requests["req-1"].Status)
	}
	if len(notifier.requestIDs) != 0 || len(service.presence.(*fakePresence).got) != 0 {
		t.Fatalf("notified %v after presence lookups %v; the reconcile pass owns both", notifier.requestIDs, service.presence.(*fakePresence).got)
	}
}

func TestRefreshDownloadsCountsPluginErrors(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 10, Downloads: 1})
	router.statusErr = errors.New("plugin unavailable")

	result, err := service.RefreshDownloads(context.Background(), 200, 0)
	if err != nil {
		t.Fatalf("RefreshDownloads: %v", err)
	}
	if result != (DownloadRefreshResult{Checked: 1, Errors: 1}) {
		t.Fatalf("result = %+v, want one request checked with an error", result)
	}
}

// A downloading target without progress is left to the reconcile pass,
// however long it stays that way: a plugin can report a title downloading with
// nothing in its download queue (Seerr keeps media at Processing for as long
// as it waits for a release), and polling it every minute would cost calls for
// nothing. A target whose progress stops leaves the pass the same way.
func TestRefreshDownloadsLeavesTargetsWithoutProgressToReconcile(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	seedDownloadTarget(t, store, "req-idle", "router-1", Quality1080p, StatusDownloading, nil)
	// req-live's 1080p has progress; its 4K, on the same plugin, has none.
	seedDownloadTarget(t, store, "req-live", "router-1", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 10, Downloads: 1})
	seedDownloadTarget(t, store, "req-live", "router-1", Quality2160p, StatusDownloading, nil)
	// The plugin still calls every target downloading, with nothing in its
	// queue.
	router.statuses = []RouterTargetStatus{
		{Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading},
		{Quality: Quality2160p, ConnectionID: "router-1", Status: StatusDownloading},
	}

	result, err := service.RefreshDownloads(context.Background(), 200, 0)
	if err != nil || result != (DownloadRefreshResult{Checked: 1}) {
		t.Fatalf("RefreshDownloads = %+v, %v; want only req-live checked", result, err)
	}
	if len(router.statusLog) != 1 || len(router.statusLog[0].refs) != 1 || router.statusLog[0].refs[0].Quality != Quality1080p {
		t.Fatalf("status calls = %+v, want one, for req-live's 1080p only", router.statusLog)
	}
	targets, _ := store.ListTargets(context.Background(), "req-live")
	for _, target := range targets {
		if target.Status != StatusDownloading || target.Download != nil {
			t.Fatalf("req-live %s target = %+v, want downloading without progress", target.Quality, target)
		}
	}

	// Neither target is polled again until the reconcile pass records new
	// progress, and a scheduled run has nothing to do.
	result, err = service.RefreshDownloads(context.Background(), 200, 0)
	if err != nil || result != (DownloadRefreshResult{}) || router.statusCalls != 1 {
		t.Fatalf("second pass = %+v, %v with %d status calls; want nothing asked", result, err, router.statusCalls)
	}
	if has, err := service.HasDownloadsToRefresh(context.Background()); err != nil || has {
		t.Fatalf("HasDownloadsToRefresh() = %v, %v; want false", has, err)
	}
}

// Progress the pass cannot refresh is stale: the pass clears it without a
// call, which takes the target off the pass.
func TestRefreshDownloadsClearsProgressItCannotRefresh(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	disabled := routerInstOn("router-3", 1)
	disabled.Enabled = false
	noKey := routerInstOn("router-4", 1)
	noKey.APIKeyRef = " "
	store.integrations = append(store.integrations, disabled, noKey)
	stale := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 500, Downloads: 1}
	for id, connection := range map[string]string{
		"req-undeclared": "router-2", // its plugin no longer declares progress
		"req-disabled":   "router-3",
		"req-no-key":     "router-4",
		"req-gone":       "router-9", // its server was deleted
	} {
		seedDownloadTarget(t, store, id, connection, Quality1080p, StatusDownloading, stale)
	}

	result, err := service.RefreshDownloads(context.Background(), 200, 0)
	if err != nil || result != (DownloadRefreshResult{}) {
		t.Fatalf("RefreshDownloads = %+v, %v; want nothing checked", result, err)
	}
	if router.statusCalls != 0 {
		t.Fatalf("status calls = %d, want none", router.statusCalls)
	}
	for _, id := range []string{"req-undeclared", "req-disabled", "req-no-key", "req-gone"} {
		if got := onlyTarget(t, store, id); got.Status != StatusDownloading || got.Download != nil {
			t.Fatalf("%s target = %+v, want downloading without progress", id, got)
		}
	}
	if left, _ := store.ListDownloadingRequests(context.Background(), 200); len(left) != 0 {
		t.Fatalf("requests left on the refresh pass = %d, want none", len(left))
	}
}

// A target that gets no status back keeps its progress while it is fresh,
// since one missed answer is usually a blip, and loses it once it is stale, so
// a frozen figure stops showing and clients stop polling for it. That holds
// whether the plugin skipped the target (its server errored or no longer has
// the title) or the whole call failed, and in the reconcile pass too.
func TestUnansweredTargetsKeepProgressUntilItIsStale(t *testing.T) {
	for _, tc := range []struct {
		name      string
		age       time.Duration
		callFails bool
		reconcile bool
		wantKept  bool
	}{
		{name: "skipped, fresh", age: 2 * time.Minute, wantKept: true},
		{name: "skipped, stale", age: staleDownloadProgress + time.Minute},
		{name: "call failed, fresh", age: 2 * time.Minute, callFails: true, wantKept: true},
		{name: "call failed, stale", age: staleDownloadProgress + time.Minute, callFails: true},
		{name: "reconcile, fresh", age: 2 * time.Minute, reconcile: true, wantKept: true},
		{name: "reconcile, stale", age: staleDownloadProgress + time.Minute, reconcile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			service, router := downloadRefreshService(store)
			reported := service.now().Add(-tc.age)
			progress := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 400, Downloads: 1, UpdatedAt: reported}
			target := seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading, progress)
			// The plugin answers for a target the request does not have.
			router.statuses = []RouterTargetStatus{{Quality: Quality2160p, ConnectionID: "router-1", Status: StatusDownloading, Progress: progress}}
			if tc.callFails {
				router.statusErr = errors.New("context deadline exceeded")
			}

			var err error
			if tc.reconcile {
				store.candidates = []*Request{store.requests["req-1"]}
				_, err = service.ReconcileRequests(context.Background(), 100)
			} else {
				_, err = service.RefreshDownloads(context.Background(), 200, 0)
			}
			if err != nil {
				t.Fatalf("pass: %v", err)
			}
			if router.statusCalls != 1 {
				t.Fatalf("status calls = %d, want 1", router.statusCalls)
			}
			got := onlyTarget(t, store, "req-1")
			if got.Status != StatusDownloading {
				t.Fatalf("status = %s, want downloading", got.Status)
			}
			if tc.wantKept {
				if !sameProgress(got.Download, progress) || !got.Download.UpdatedAt.Equal(reported) {
					t.Fatalf("progress = %+v, want %+v as last reported", got.Download, progress)
				}
				if !slices.Equal(store.downloadChecks, []int64{target.ID}) || len(store.downloadWrites) != 0 {
					t.Fatalf("checks = %v, writes = %+v; want the target marked asked about and nothing else", store.downloadChecks, store.downloadWrites)
				}
				return
			}
			if got.Download != nil {
				t.Fatalf("progress = %+v, want stale progress cleared", got.Download)
			}
			if len(store.downloadChecks) != 0 {
				t.Fatalf("checks = %v, want none once the progress is cleared", store.downloadChecks)
			}
		})
	}
}

// A request whose server stops answering takes its turn and goes to the back
// instead of heading every batch, so the others still get refreshed.
func TestRefreshDownloadsRotatesPastATargetWithoutAnAnswer(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	store.integrations = append(store.integrations, routerInstOn("router-3", 1))
	now := time.Now().UTC()
	seedDownloadTarget(t, store, "req-silent", "router-1", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1, UpdatedAt: now.Add(-2 * time.Minute)})
	seedDownloadTarget(t, store, "req-healthy", "router-3", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1, UpdatedAt: now.Add(-time.Minute)})
	// router-1 has stopped reporting its target; router-3 answers.
	progress := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 300, Downloads: 1}
	router.statuses = []RouterTargetStatus{{Quality: Quality1080p, ConnectionID: "router-3", Status: StatusDownloading, Progress: progress}}

	for range 2 {
		if _, err := service.RefreshDownloads(context.Background(), 1, 0); err != nil {
			t.Fatalf("RefreshDownloads: %v", err)
		}
	}
	if len(router.statusLog) != 2 || router.statusLog[0].conns[0].ID != "router-1" || router.statusLog[1].conns[0].ID != "router-3" {
		t.Fatalf("status calls = %+v, want router-1's request and then router-3's", router.statusLog)
	}
	if got := onlyTarget(t, store, "req-healthy"); !sameProgress(got.Download, progress) {
		t.Fatalf("healthy progress = %+v, want %+v", got.Download, progress)
	}
}

// A target whose plugin's features cannot be read moves to the back of the
// rotation like one its server did not answer for, so a lasting failure does
// not keep the other downloads from being refreshed.
func TestRefreshDownloadsRotatesPastAFeatureReadFailure(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	store.integrations = append(store.integrations, routerInstOn("router-3", 1))
	now := time.Now().UTC()
	seedDownloadTarget(t, store, "req-first", "router-1", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1, UpdatedAt: now.Add(-2 * time.Minute)})
	seedDownloadTarget(t, store, "req-second", "router-3", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1, UpdatedAt: now.Add(-time.Minute)})
	router.featuresErr = errors.New("capability metadata unavailable")

	result, err := service.RefreshDownloads(context.Background(), 1, 0)
	if err != nil || result.Errors != 1 {
		t.Fatalf("RefreshDownloads = %+v, %v; want one error", result, err)
	}
	router.featuresErr = nil
	if _, err := service.RefreshDownloads(context.Background(), 1, 0); err != nil {
		t.Fatalf("RefreshDownloads: %v", err)
	}
	if len(router.statusLog) != 1 || router.statusLog[0].conns[0].ID != "router-3" {
		t.Fatalf("status calls = %+v, want req-second's once req-first moved back", router.statusLog)
	}
}

// A pass ends within its budget even while a download server has stopped
// answering and each call to it would run to the router's deadline. The call
// in flight is cut, its target goes to the back of the rotation, and the
// requests left over wait for the next pass without failing this one.
func TestRefreshDownloadsStopsWhenItsBudgetRunsOut(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	store.integrations = append(store.integrations, routerInstOn("router-3", 3))
	router.progressCapable[3] = true
	router.statusHangFor = map[int]bool{1: true}
	now := time.Now().UTC()
	progress := func(age time.Duration) *DownloadProgress {
		return &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1, UpdatedAt: now.Add(-age)}
	}
	hungA := seedDownloadTarget(t, store, "req-hung-a", "router-1", Quality1080p, StatusDownloading, progress(3*time.Minute))
	seedDownloadTarget(t, store, "req-hung-b", "router-1", Quality1080p, StatusDownloading, progress(2*time.Minute))
	seedDownloadTarget(t, store, "req-healthy", "router-3", Quality1080p, StatusDownloading, progress(time.Minute))

	type outcome struct {
		result DownloadRefreshResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.RefreshDownloads(context.Background(), 200, 50*time.Millisecond)
		done <- outcome{result, err}
	}()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RefreshDownloads outlasted its budget while a server hung")
	}
	if got.err != nil || got.result != (DownloadRefreshResult{Checked: 1, Errors: 1}) {
		t.Fatalf("RefreshDownloads = %+v, %v; want the one cut call and no error", got.result, got.err)
	}
	if router.statusCalls != 1 {
		t.Fatalf("status calls = %d, want only the call the budget cut", router.statusCalls)
	}
	if !slices.Equal(store.downloadChecks, []int64{hungA.ID}) || onlyTarget(t, store, "req-hung-a").Download == nil {
		t.Fatalf("checks = %v; want the cut request's target marked asked about, its progress kept", store.downloadChecks)
	}
	next, err := store.ListDownloadingRequests(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if ids, want := func() []string {
		ids := make([]string, 0, len(next))
		for _, req := range next {
			ids = append(ids, req.ID)
		}
		return ids
	}(), []string{"req-hung-b", "req-healthy", "req-hung-a"}; !slices.Equal(ids, want) {
		t.Fatalf("next pass order = %v, want %v", ids, want)
	}
}

func TestHasDownloadsToRefresh(t *testing.T) {
	store := newFakeStore()
	seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 10, Downloads: 1})
	if has, err := newTestService(store).HasDownloadsToRefresh(context.Background()); err != nil || has {
		t.Fatalf("without a router: %v, %v; want false", has, err)
	}

	store = newFakeStore()
	service, _ := downloadRefreshService(store)
	seedDownloadTarget(t, store, "req-idle", "router-1", Quality1080p, StatusDownloading, nil)
	seedDownloadTarget(t, store, "req-queued", "router-1", Quality1080p, StatusQueued,
		&DownloadProgress{Phase: DownloadPhaseQueued, Downloads: 1})
	if has, err := service.HasDownloadsToRefresh(context.Background()); err != nil || has {
		t.Fatalf("with no downloading target that has progress: %v, %v; want false", has, err)
	}
	seedDownloadTarget(t, store, "req-live", "router-1", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 10, Downloads: 1})
	if has, err := service.HasDownloadsToRefresh(context.Background()); err != nil || !has {
		t.Fatalf("with a downloading target that has progress: %v, %v; want true", has, err)
	}
}

func TestRefreshDownloadsWithoutWorkMakesNoCalls(t *testing.T) {
	store := newFakeStore()
	seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading, nil)
	if result, err := newTestService(store).RefreshDownloads(context.Background(), 200, 0); err != nil || result != (DownloadRefreshResult{}) {
		t.Fatalf("without a router: result = %+v, err = %v", result, err)
	}

	store = newFakeStore()
	service, router := downloadRefreshService(store)
	store.listIntegrationsCalls = 0
	if result, err := service.RefreshDownloads(context.Background(), 200, 0); err != nil || result != (DownloadRefreshResult{}) {
		t.Fatalf("with nothing downloading: result = %+v, err = %v", result, err)
	}
	if store.listIntegrationsCalls != 0 || router.statusCalls != 0 {
		t.Fatalf("integrations reads = %d, status calls = %d; want none while nothing downloads", store.listIntegrationsCalls, router.statusCalls)
	}
}

func TestGetDetailCarriesTheActiveRequestDownload(t *testing.T) {
	store := newFakeStore()
	eta := time.Date(2026, 5, 24, 13, 0, 0, 0, time.UTC)
	seedDownloadTarget(t, store, "req-1", "router-1", Quality1080p, StatusDownloading,
		&DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 250, Downloads: 1, EstimatedCompletion: &eta})
	active := store.requests["req-1"]
	active.RequestedByUserID, active.RequestedByProfileID = 2, "profile-2"
	store.active[MediaTypeMovie][550] = active
	service := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{MediaType: "movie", ID: 550, Title: "Fight Club"}})

	detail, err := service.GetDetail(context.Background(), testViewer(1), MediaTypeMovie, 550)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	want := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 250, Downloads: 1, EstimatedCompletion: &eta}
	if !sameProgress(detail.Request.Download, want) {
		t.Fatalf("detail request download = %+v, want %+v", detail.Request.Download, want)
	}

	// A request that has not reached a download server has none.
	active.Status = StatusApproved
	if detail, err = service.GetDetail(context.Background(), testViewer(1), MediaTypeMovie, 550); err != nil || detail.Request.Download != nil {
		t.Fatalf("approved request: download = %+v, err = %v; want none", detail.Request.Download, err)
	}
}

// The raw status write changes nothing but external_status: not the date of
// the target's last status change, the request, or its history. A target that
// is no longer queued or downloading keeps what it has.
func TestUpdateTargetExternalStatusDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req-ext", 1, 802, StatusDownloading)
	target, err := repo.CreateTarget(ctx, Target{RequestID: "req-ext", Quality: Quality1080p, Status: StatusDownloading, ExternalStatus: "completed/importBlocked"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_request_targets SET updated_at = now() - interval '2 days' WHERE id = $1`, target.ID); err != nil {
		t.Fatal(err)
	}
	read := func() (external string, updated time.Time, events int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT t.external_status, t.updated_at, (SELECT count(*) FROM media_request_events e WHERE e.request_id = t.request_id)
			FROM media_request_targets t WHERE t.id = $1`, target.ID).Scan(&external, &updated, &events); err != nil {
			t.Fatal(err)
		}
		return external, updated, events
	}
	_, beforeUpdated, beforeEvents := read()

	if err := repo.UpdateTargetExternalStatus(ctx, target.ID, "warning/downloading"); err != nil {
		t.Fatal(err)
	}
	if external, updated, events := read(); external != "warning/downloading" || !updated.Equal(beforeUpdated) || events != beforeEvents {
		t.Fatalf("after write: external_status %q, updated_at %v (was %v), events %d (was %d); want the new status and nothing else changed",
			external, updated, beforeUpdated, events, beforeEvents)
	}

	if _, err := pool.Exec(ctx, `UPDATE media_request_targets SET status = 'completed' WHERE id = $1`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateTargetExternalStatus(ctx, target.ID, "queued"); err != nil {
		t.Fatal(err)
	}
	if external, _, _ := read(); external != "warning/downloading" {
		t.Fatalf("a completed target's external_status = %q, want it left alone", external)
	}
}

func TestUpdateTargetDownloadDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req-dl", 1, 801, StatusDownloading)
	target, err := repo.CreateTarget(ctx, Target{RequestID: "req-dl", Quality: Quality1080p, Status: StatusDownloading})
	if err != nil {
		t.Fatal(err)
	}
	// Date the target's and the request's last change, and their history.
	if _, err := pool.Exec(ctx, `UPDATE media_request_targets SET updated_at = now() - interval '2 days' WHERE id = $1`, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET updated_at = now() - interval '2 days' WHERE id = 'req-dl'`); err != nil {
		t.Fatal(err)
	}
	snapshot := func() (targetUpdated, requestUpdated time.Time, events int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT t.updated_at, r.updated_at, (SELECT count(*) FROM media_request_events e WHERE e.request_id = r.id)
			FROM media_request_targets t JOIN media_requests r ON r.id = t.request_id
			WHERE t.id = $1`, target.ID).Scan(&targetUpdated, &requestUpdated, &events); err != nil {
			t.Fatal(err)
		}
		return targetUpdated, requestUpdated, events
	}
	stored := func() *DownloadProgress {
		t.Helper()
		targets, err := repo.ListTargets(ctx, "req-dl")
		if err != nil || len(targets) != 1 {
			t.Fatalf("targets = %+v, err = %v", targets, err)
		}
		return targets[0].Download
	}
	beforeTarget, beforeRequest, beforeEvents := snapshot()

	eta := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	progress := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 5000, BytesLeft: 1200, Downloads: 2, EstimatedCompletion: &eta}
	written := time.Now()
	if err := repo.UpdateTargetDownload(ctx, target.ID, progress); err != nil {
		t.Fatal(err)
	}
	got := stored()
	if !sameProgress(got, progress) || got.UpdatedAt.Before(written.Add(-time.Minute)) {
		t.Fatalf("stored progress = %+v, want %+v stamped now", got, progress)
	}
	// A report counts as asked about too, for the refresh rotation.
	checkedAt := func() *time.Time {
		t.Helper()
		var at *time.Time
		if err := pool.QueryRow(ctx, `SELECT download_checked_at FROM media_request_targets WHERE id = $1`, target.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	if at := checkedAt(); at == nil || !at.Equal(got.UpdatedAt) {
		t.Fatalf("download_checked_at = %v, want the report's %v", at, got.UpdatedAt)
	}
	if afterTarget, afterRequest, afterEvents := snapshot(); !afterTarget.Equal(beforeTarget) || !afterRequest.Equal(beforeRequest) || afterEvents != beforeEvents {
		t.Fatalf("a progress write moved updated_at (target %v -> %v, request %v -> %v) or wrote history (%d -> %d)",
			beforeTarget, afterTarget, beforeRequest, afterRequest, beforeEvents, afterEvents)
	}

	// Nil clears it, again without touching the target or the request.
	if err := repo.UpdateTargetDownload(ctx, target.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := stored(); got != nil {
		t.Fatalf("progress after clearing = %+v, want none", got)
	}
	if at := checkedAt(); at != nil {
		t.Fatalf("download_checked_at after clearing = %v, want none", at)
	}
	if afterTarget, _, _ := snapshot(); !afterTarget.Equal(beforeTarget) {
		t.Fatal("clearing progress moved the target's updated_at")
	}

	// Completion clears it, and a late report cannot bring it back.
	if err := repo.UpdateTargetDownload(ctx, target.ID, progress); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateTargetStatus(ctx, target.ID, StatusCompleted, "", "imported", "", Viewer{}); err != nil {
		t.Fatal(err)
	}
	if got := stored(); got != nil {
		t.Fatalf("progress after completion = %+v, want none", got)
	}
	if err := repo.UpdateTargetDownload(ctx, target.ID, progress); err != nil {
		t.Fatal(err)
	}
	if got := stored(); got != nil {
		t.Fatalf("a late report wrote %+v onto a completed target", got)
	}
}

func TestFailedTargetLosesDownloadProgressDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req-fail", 1, 811, StatusQueued)
	target, err := repo.CreateTarget(ctx, Target{RequestID: "req-fail", Quality: Quality1080p, Status: StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateTargetDownload(ctx, target.ID, &DownloadProgress{Phase: DownloadPhaseStalled, BytesTotal: 10, BytesLeft: 10, Downloads: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateTargetStatus(ctx, target.ID, StatusFailed, "", "", "download failed", Viewer{}); err != nil {
		t.Fatal(err)
	}
	targets, err := repo.ListTargets(ctx, "req-fail")
	if err != nil || len(targets) != 1 || targets[0].Download != nil {
		t.Fatalf("targets = %+v, err = %v; want the failed target without progress", targets, err)
	}
}

// addDownloadTestTarget stores a target for a DB test, with progress reported
// and asked about refreshedAgo ago, or without progress when refreshedAgo is
// empty.
func addDownloadTestTarget(t *testing.T, repo *Repository, pool *pgxpool.Pool, requestID string, quality Quality, status Status, refreshedAgo string) Target {
	t.Helper()
	ctx := t.Context()
	target, err := repo.CreateTarget(ctx, Target{RequestID: requestID, Quality: quality, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	if refreshedAgo == "" {
		return target
	}
	if _, err := pool.Exec(ctx, `
		UPDATE media_request_targets
		SET download_phase = 'downloading', download_bytes_total = 1, download_bytes_left = 1,
		    download_count = 1, download_updated_at = now() - $2::interval,
		    download_checked_at = now() - $2::interval
		WHERE id = $1`, target.ID, refreshedAgo); err != nil {
		t.Fatal(err)
	}
	return target
}

func requestIDs(reqs []*Request) []string {
	ids := make([]string, 0, len(reqs))
	for _, req := range reqs {
		ids = append(ids, req.ID)
	}
	return ids
}

func TestListDownloadingRequestsOrderDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	addTarget := func(requestID string, quality Quality, status Status, refreshedAgo string) {
		t.Helper()
		_ = addDownloadTestTarget(t, repo, pool, requestID, quality, status, refreshedAgo)
	}
	for i, id := range []string{"dl-a", "dl-b", "dl-c", "dl-d", "dl-e", "dl-f", "dl-g", "dl-h"} {
		insertLifecycleRequest(t, repo, id, 1, 900+i, StatusDownloading)
	}
	addTarget("dl-a", Quality1080p, StatusDownloading, "1 minute")
	addTarget("dl-b", Quality1080p, StatusDownloading, "")      // no progress
	addTarget("dl-c", Quality1080p, StatusQueued, "30 minutes") // nothing downloading
	addTarget("dl-d", Quality1080p, StatusDownloading, "10 minutes")
	// One recent target does not hide a stale sibling.
	addTarget("dl-e", Quality1080p, StatusDownloading, "30 seconds")
	addTarget("dl-e", Quality2160p, StatusDownloading, "5 minutes")
	// A sibling without progress does not make the request look stale.
	addTarget("dl-f", Quality1080p, StatusDownloading, "2 minutes")
	addTarget("dl-f", Quality2160p, StatusDownloading, "")
	addTarget("dl-g", Quality1080p, StatusDownloading, "20 minutes")
	addTarget("dl-h", Quality1080p, StatusDownloading, "") // no progress
	// A closed request is not refreshed.
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'cancelled' WHERE id = 'dl-g'`); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListDownloadingRequests(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ids, want := requestIDs(got), []string{"dl-d", "dl-e", "dl-f", "dl-a"}; !slices.Equal(ids, want) {
		t.Fatalf("downloading requests = %v, want %v", ids, want)
	}
	limited, err := repo.ListDownloadingRequests(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ids, want := requestIDs(limited), []string{"dl-d", "dl-e"}; !slices.Equal(ids, want) {
		t.Fatalf("limited = %v, want the two refreshed longest ago", ids)
	}
}

// Downloading targets that never report progress (a plugin that does not
// declare it, or one with nothing queued for them) cannot fill the batch and
// starve the targets the refresh pass exists for, however many there are.
func TestListDownloadingRequestsSkipsTargetsWithoutProgressDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	const limit = 3
	for i := range 2 * limit {
		id := fmt.Sprintf("dl-idle-%d", i)
		insertLifecycleRequest(t, repo, id, 1, 950+i, StatusDownloading)
		_ = addDownloadTestTarget(t, repo, pool, id, Quality1080p, StatusDownloading, "")
	}
	insertLifecycleRequest(t, repo, "dl-live-1", 1, 970, StatusDownloading)
	_ = addDownloadTestTarget(t, repo, pool, "dl-live-1", Quality1080p, StatusDownloading, "1 minute")
	insertLifecycleRequest(t, repo, "dl-live-2", 1, 971, StatusDownloading)
	_ = addDownloadTestTarget(t, repo, pool, "dl-live-2", Quality1080p, StatusDownloading, "3 minutes")

	got, err := repo.ListDownloadingRequests(ctx, limit)
	if err != nil {
		t.Fatal(err)
	}
	if ids, want := requestIDs(got), []string{"dl-live-2", "dl-live-1"}; !slices.Equal(ids, want) {
		t.Fatalf("downloading requests = %v, want %v", ids, want)
	}
}

// A target whose server stops answering takes its turn and goes to the back of
// the rotation, keeping its progress and when it was last heard from; it does
// not head every batch.
func TestMarkTargetDownloadCheckedRotatesDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "dl-silent", 1, 980, StatusDownloading)
	silent := addDownloadTestTarget(t, repo, pool, "dl-silent", Quality1080p, StatusDownloading, "10 minutes")
	insertLifecycleRequest(t, repo, "dl-healthy", 1, 981, StatusDownloading)
	_ = addDownloadTestTarget(t, repo, pool, "dl-healthy", Quality1080p, StatusDownloading, "1 minute")
	if _, err := pool.Exec(ctx, `UPDATE media_request_targets SET updated_at = now() - interval '2 days' WHERE id = $1`, silent.ID); err != nil {
		t.Fatal(err)
	}
	read := func() (heard time.Time, updated time.Time, progress *DownloadProgress) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT download_updated_at, updated_at FROM media_request_targets WHERE id = $1`, silent.ID).Scan(&heard, &updated); err != nil {
			t.Fatal(err)
		}
		targets, err := repo.ListTargets(ctx, "dl-silent")
		if err != nil || len(targets) != 1 {
			t.Fatalf("targets = %+v, err = %v", targets, err)
		}
		return heard, updated, targets[0].Download
	}
	heardBefore, updatedBefore, _ := read()

	if got, err := repo.ListDownloadingRequests(ctx, 10); err != nil || !slices.Equal(requestIDs(got), []string{"dl-silent", "dl-healthy"}) {
		t.Fatalf("before = %v, %v; want the silent request first", requestIDs(got), err)
	}
	if err := repo.MarkTargetDownloadChecked(ctx, silent.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.ListDownloadingRequests(ctx, 10); err != nil || !slices.Equal(requestIDs(got), []string{"dl-healthy", "dl-silent"}) {
		t.Fatalf("after = %v, %v; want the silent request behind the healthy one", requestIDs(got), err)
	}
	heard, updated, progress := read()
	if !heard.Equal(heardBefore) || !updated.Equal(updatedBefore) || progress == nil || progress.Phase != DownloadPhaseDownloading {
		t.Fatalf("heard %v -> %v, updated_at %v -> %v, progress %+v; want all kept", heardBefore, heard, updatedBefore, updated, progress)
	}

	// A target without progress, or finished, is not stamped.
	bare := addDownloadTestTarget(t, repo, pool, "dl-healthy", Quality2160p, StatusDownloading, "")
	if err := repo.MarkTargetDownloadChecked(ctx, bare.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateTargetStatus(ctx, silent.ID, StatusCompleted, "", "imported", "", Viewer{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkTargetDownloadChecked(ctx, silent.ID); err != nil {
		t.Fatal(err)
	}
	var stamped int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_request_targets WHERE id = ANY($1) AND download_checked_at IS NOT NULL`, []int64{bare.ID, silent.ID}).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if stamped != 0 {
		t.Fatalf("%d targets without live progress were stamped, want none", stamped)
	}
}
