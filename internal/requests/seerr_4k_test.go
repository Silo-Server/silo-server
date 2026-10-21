package requests

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

// seerrRouter fulfills like the Seerr plugin: it sends 2160p only to the
// connections whose "This Seerr handles 4K requests" switch is on, and
// returns no 2160p target when none has it.
type seerrRouter struct{ *fakeRouterProvider }

func (r seerrRouter) Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	targets, msg, err := r.fakeRouterProvider.Fulfill(ctx, installationID, capabilityID, req, qualities, conns)
	var out []RouterTarget
	for _, t := range targets {
		if t.Quality != Quality2160p {
			out = append(out, t)
			continue
		}
		for _, conn := range conns {
			if supports4K, _ := conn.Config["supports_4k"].(bool); supports4K {
				t.ConnectionID = conn.ID
				out = append(out, t)
				break
			}
		}
	}
	return out, msg, err
}

func seerrInst(id string, supports4K bool) Integration {
	in := routerInst(id)
	in.CapabilityID = "seerr"
	in.PluginConfig = map[string]any{"supports_4k": supports4K, "requester_mode": "admin", "auto_approve": true}
	return in
}

// Issue #2633: a requester with no quality cap must not get a failed 4K
// target from a Seerr that does not take 4K requests.
func TestSubmitSkips4KOnSeerrWithout4K(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{seerrInst("seerr", false)}
	router := seerrRouter{&fakeRouterProvider{}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: ""})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	got, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !slices.Equal(router.gotQualities, []Quality{Quality1080p}) {
		t.Fatalf("qualities = %v, want [1080p] from a Seerr without 4K", router.gotQualities)
	}
	if got.Outcome != OutcomeActive || got.Status != StatusQueued {
		t.Fatalf("request = %s/%s, want queued/active", got.Status, got.Outcome)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 || targets[0].Quality != Quality1080p || targets[0].Status != StatusQueued {
		t.Fatalf("targets = %+v, want one queued 1080p target", targets)
	}

	// Seerr finishes the HD copy: reconciliation completes the request.
	router.statuses = []RouterTargetStatus{{Quality: Quality1080p, ConnectionID: "seerr", Status: StatusCompleted}}
	store.candidates = []*Request{store.requests["r1"]}
	if _, err := svc.ReconcileRequests(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	if done := store.requests["r1"]; done.Status != StatusCompleted || done.Outcome != OutcomeActive {
		t.Fatalf("request after reconcile = %s/%s, want completed/active", done.Status, done.Outcome)
	}
}

// A Seerr that takes 4K still gets 4K requests, also next to one that does
// not, whichever order the connections come in.
func TestSubmitSends4KToSeerrThatTakes4K(t *testing.T) {
	for _, order := range [][]bool{{true}, {false, true}, {true, false}} {
		store := newFakeStore()
		for i, supports4K := range order {
			store.integrations = append(store.integrations, seerrInst(fmt.Sprintf("seerr-%d", i), supports4K))
		}
		router := seerrRouter{&fakeRouterProvider{}}
		svc := newTestService(store)
		svc.SetRouterProvider(router)
		svc.SetEntitlementResolver(fixedCeiling{q: ""})

		req := Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
		store.requests["r1"] = &req
		if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
			t.Fatalf("submit: %v", err)
		}
		if !slices.Equal(router.gotQualities, []Quality{Quality1080p, Quality2160p}) {
			t.Fatalf("4K switches %v: qualities = %v, want [1080p 2160p]", order, router.gotQualities)
		}
		targets, _ := store.ListTargets(context.Background(), "r1")
		for _, target := range targets {
			if target.Status == StatusFailed {
				t.Fatalf("4K switches %v: targets = %+v, want none failed", order, targets)
			}
		}
	}
}

// Force-dual asks every request for 4K, so a Seerr without 4K still shows the
// missing copy as failed, the same as a Radarr setup with no 4K server.
func TestForceDualStillAsksSeerrWithout4KFor4K(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{seerrInst("seerr", false)}
	store.settings.ForceDualQuality = true
	router := seerrRouter{&fakeRouterProvider{}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: "1080p"})

	req := Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive, RequestedByUserID: 7}
	store.requests["r1"] = &req
	if _, err := svc.submitApprovedRequest(context.Background(), req, Viewer{UserID: 7, IsAdmin: true}, nil); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !slices.Equal(router.gotQualities, []Quality{Quality1080p, Quality2160p}) {
		t.Fatalf("qualities = %v, want [1080p 2160p] under force-dual", router.gotQualities)
	}
}

// A request that already failed on the unwanted 4K target completes on Retry
// once its HD copy is done, without asking Seerr for 4K again.
func TestRetryClearsUnwanted4KFailureOnSeerrWithout4K(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{seerrInst("seerr", false)}
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeFailed, RequestedByUserID: 7}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "seerr", Quality: Quality1080p, Status: StatusCompleted},
		{ID: 2, RequestID: "r1", Quality: Quality2160p, Status: StatusFailed, LastError: msgNoTargetForQuality},
	}}
	store.targetSeq = 2
	router := seerrRouter{&fakeRouterProvider{}}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: ""})

	req, err := svc.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "r1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d (qualities %v), want 0", router.fulfillCalls, router.gotQualities)
	}
	if req.Status != StatusCompleted || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want completed/active", req.Status, req.Outcome)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 || targets[0].Quality != Quality1080p {
		t.Fatalf("targets = %+v, want only the 1080p target", targets)
	}
}
