package requests

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// keylessRouterInst is a connection on installation 1 saved without an API
// key, the way an admin sets up a server for a router that needs none.
func keylessRouterInst(id string) Integration {
	in := routerInst(id)
	in.APIKeyRef = ""
	return in
}

// A router that declares api_key_optional gets requests for a server saved
// without a key, with an empty key, instead of the request waiting for one.
// TestCreateRequestAutoApprovalDefersOnKeylessConnection covers a router
// without the declaration.
func TestCreateRequestAutoApprovalSendsToKeyOptionalRouter(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	store.integrations = []Integration{keylessRouterInst("router-1")}
	service := newTestService(store)
	router := &fakeRouterProvider{keyOptional: map[int]bool{1: true}}
	service.SetRouterProvider(router)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if router.fulfillCalls != 1 || len(router.gotConns) != 1 || router.gotConns[0].ID != "router-1" || router.gotConns[0].APIKey != "" {
		t.Fatalf("fulfill calls = %d, connections = %+v; want one call to router-1 without a key", router.fulfillCalls, router.gotConns)
	}
	if req.Status == StatusApproved || req.LastError != "" {
		t.Fatalf("request = %+v, want it sent to the router", req)
	}
}

// When the router's declaration cannot be read and no other connection can
// take the request, the submission retries with the read error.
func TestSubmitDefersWhenKeyOptionalDeclarationCannotBeRead(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{keylessRouterInst("router-1")}
	router := &fakeRouterProvider{keyOptional: map[int]bool{1: true}, featuresErr: errors.New("capabilities unavailable")}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want none", router.fulfillCalls)
	}
	if got.Status != StatusApproved || got.Outcome != OutcomeActive || !strings.Contains(got.LastError, "capabilities unavailable") {
		t.Fatalf("request = %+v, want it approved and waiting to retry", got)
	}
}

// A connection whose plugin's declaration cannot be read is skipped like one
// without a key, so a sibling with a key still takes the request.
func TestSubmitUsesKeyedSiblingWhenKeyOptionalDeclarationCannotBeRead(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{keylessRouterInst("router-keyless"), routerInst("router-keyed")}
	router := &fakeRouterProvider{featuresErr: errors.New("capabilities unavailable")}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req := Request{ID: "r1", MediaType: MediaTypeMovie, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if router.fulfillCalls != 1 || len(router.gotConns) != 1 || router.gotConns[0].ID != "router-keyed" {
		t.Fatalf("fulfill calls = %d, connections = %+v; want one call to router-keyed", router.fulfillCalls, router.gotConns)
	}
}

// A route sends to a server without a key when its router needs none, and
// still refuses one whose router needs a key.
func TestSubmitRoutedHonoursKeyOptionalRouter(t *testing.T) {
	for _, tc := range []struct {
		name        string
		keyOptional bool
	}{
		{"router needs no key", true},
		{"router needs a key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := routingStore(capturedFacts(RoutingFacts{}))
			for i := range store.integrations {
				store.integrations[i].APIKeyRef = ""
			}
			router := &fakeRouterProvider{keyOptional: map[int]bool{1: tc.keyOptional}}
			svc := newTestService(store)
			svc.SetRouterProvider(router)
			svc.SetEntitlementResolver(fixedCeiling{q: "1080p"})

			got, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil)
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if !tc.keyOptional {
				if len(router.fulfillLog) != 0 || !strings.Contains(got.LastError, msgRouterNoKey) {
					t.Fatalf("fulfill calls = %+v, last error = %q; want none and the missing-key reason", router.fulfillLog, got.LastError)
				}
				return
			}
			if len(router.fulfillLog) != 1 || len(router.fulfillLog[0].conns) != 1 || router.fulfillLog[0].conns[0].APIKey != "" {
				t.Fatalf("fulfill calls = %+v, want one call to the routed server without a key", router.fulfillLog)
			}
		})
	}
}

// The route preview does not flag a server without a key when its router
// needs none.
func TestPreviewRouteAcceptsKeyOptionalServer(t *testing.T) {
	store := routingStore(RoutingFacts{})
	store.integrations[0].APIKeyRef = "" // radarr-hd, the fallback's HD server
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{ID: 129}})
	svc.SetRouterProvider(&fakeRouterProvider{keyOptional: map[int]bool{1: true}})

	preview, err := svc.PreviewRoute(context.Background(), routeAdmin, MediaTypeMovie, 129, 7)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, tier := range preview.Tiers {
		if tier.Reason != "" {
			t.Fatalf("%s tier = %+v, want no failure note", tier.Quality, tier)
		}
	}
}

// The reconcile pass asks a key-optional router about a target on a server
// without a key.
func TestReconcileRequestsChecksTargetOnKeyOptionalServer(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{keylessRouterInst("router-1")}
	store.candidates = []*Request{{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeActive}}
	store.requests["req-1"] = &Request{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeActive}
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: "req-1", IntegrationID: "router-1",
		Quality: Quality1080p, Status: StatusQueued, ExternalID: "123",
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	router := &fakeRouterProvider{
		keyOptional: map[int]bool{1: true},
		statuses:    []RouterTargetStatus{{Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading, ExternalStatus: "downloading"}},
	}
	service := newTestService(store)
	service.SetRouterProvider(router)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if router.statusCalls != 1 || len(router.statusLog) != 1 || router.statusLog[0].conns[0].APIKey != "" {
		t.Fatalf("status calls = %+v, want one call without a key", router.statusLog)
	}
	if result.Downloading != 1 {
		t.Fatalf("result = %+v, want the target marked downloading", result)
	}
}

// The download refresh pass polls a key-optional router that reports
// progress for a target on a server without a key, and keeps the progress.
func TestRefreshDownloadsPollsKeyOptionalServer(t *testing.T) {
	store := newFakeStore()
	service, router := downloadRefreshService(store)
	store.integrations = []Integration{keylessRouterInst("router-1")}
	router.keyOptional = map[int]bool{1: true}
	earlier := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 900, Downloads: 1}
	seedDownloadTarget(t, store, "req-keyless", "router-1", Quality1080p, StatusDownloading, earlier)
	progress := &DownloadProgress{Phase: DownloadPhaseDownloading, BytesTotal: 1000, BytesLeft: 400, Downloads: 1}
	router.statuses = []RouterTargetStatus{{Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading, Progress: progress}}

	result, err := service.RefreshDownloads(context.Background(), 200, 0)
	if err != nil {
		t.Fatalf("RefreshDownloads: %v", err)
	}
	if result != (DownloadRefreshResult{Checked: 1}) || router.statusCalls != 1 {
		t.Fatalf("result = %+v, status calls = %d; want one request checked", result, router.statusCalls)
	}
	if got := onlyTarget(t, store, "req-keyless"); !sameProgress(got.Download, progress) {
		t.Fatalf("progress = %+v, want %+v", got.Download, progress)
	}
}

// Testing a new server for a key-optional router reaches the plugin without
// a key or an address.
func TestLoadIntegrationOptionsProbesKeyOptionalRouterWithoutKey(t *testing.T) {
	store := newFakeStore()
	router := &fakeRouterProvider{keyOptional: map[int]bool{1: true}}
	service := newTestService(store)
	service.SetRouterProvider(router)
	install := 1

	if _, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:             "new",
		CapabilityID:   "arr",
		InstallationID: &install,
	}); err != nil {
		t.Fatalf("LoadIntegrationOptions: %v", err)
	}
	if router.gotOptionsConn.ID != "new" || router.gotOptionsConn.APIKey != "" || router.gotOptionsConn.BaseURL != "" {
		t.Fatalf("probe conn = %+v, want the new server without a key or address", router.gotOptionsConn)
	}
}

// A key-optional router is probed at a changed address without the saved
// key: the key stays with the address it was saved for.
func TestLoadIntegrationOptionsKeepsSavedKeyFromChangedAddressOfKeyOptionalRouter(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	router := &fakeRouterProvider{keyOptional: map[int]bool{1: true}}
	service := newTestService(store)
	service.SetRouterProvider(router)

	if _, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
		ID:      "router-1",
		BaseURL: "http://elsewhere.example",
	}); err != nil {
		t.Fatalf("LoadIntegrationOptions: %v", err)
	}
	if router.gotOptionsConn.BaseURL != "http://elsewhere.example" || router.gotOptionsConn.APIKey != "" {
		t.Fatalf("probe conn = %+v, want the changed address without the saved key", router.gotOptionsConn)
	}
}

// A server saved without a key has no key to protect, so its address can
// change without one; a saved key must still be typed again for a new
// address, whatever the router declares.
func TestUpdateIntegrationAddressChangeNeedsKeyOnlyWhenOneIsSaved(t *testing.T) {
	for _, tc := range []struct {
		name      string
		saved     Integration
		wantError bool
	}{
		{"saved without a key", keylessRouterInst("router-1"), false},
		{"saved with a key", routerInst("router-1"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.integrations = []Integration{tc.saved}
			router := &fakeRouterProvider{keyOptional: map[int]bool{1: true}}
			service := newTestService(store)
			service.SetRouterProvider(router)
			install := 1

			_, err := service.UpdateIntegration(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
				ID:             "router-1",
				Name:           "router-1",
				Enabled:        true,
				CapabilityID:   "arr",
				BaseURL:        "http://moved.local",
				InstallationID: &install,
			})
			var ve *ValidationError
			if tc.wantError {
				if !errors.As(err, &ve) || ve.FieldErrors["api_key_ref"] == "" || router.validateCalls != 0 {
					t.Fatalf("err = %v, validate calls = %d; want the api_key_ref field error and no validation", err, router.validateCalls)
				}
				return
			}
			if err != nil || router.validateCalls != 1 {
				t.Fatalf("err = %v, validate calls = %d; want the change validated by the plugin", err, router.validateCalls)
			}
		})
	}
}
