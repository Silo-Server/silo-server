package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

func intPtr(v int) *int { return &v }

// groupedService is a service whose test account belongs to access group 1.
func groupedService(store *fakeStore, user *models.User) *Service {
	svc := newTestService(store)
	if user != nil {
		svc.SetUserRepository(requestUserRepo{user: user})
	}
	svc.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: true}})
	return svc
}

func TestEffectivePolicyLayersAccountGroupServer(t *testing.T) {
	store := newFakeStore()
	store.settings.GlobalAutoApprovalEnabled = true
	store.groupLimits = map[int64]*GroupLimit{1: {GroupID: 1, LimitMode: LimitModeCustom, MaxRequests: intPtr(10), WindowDays: intPtr(30), ApprovalMode: ApprovalModeManual}}
	svc := groupedService(store, nil)

	policy, err := svc.EffectivePolicy(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if policy.MaxRequests != 10 || policy.WindowDays != 30 || policy.AutoApprove || policy.Blocked {
		t.Fatalf("group layer: %+v, want 10 per 30 days, manual", policy)
	}

	// The account's own settings win where they are set, and only there.
	store.limit = &UserLimit{UserID: 1, LimitMode: LimitModeInherit, ApprovalMode: ApprovalModeAuto}
	if policy, _ = svc.EffectivePolicy(context.Background(), 1); policy.MaxRequests != 10 || !policy.AutoApprove {
		t.Fatalf("account approval: %+v, want the group's limit and automatic approval", policy)
	}
	store.limit = &UserLimit{UserID: 1, LimitMode: LimitModeUnlimited, ApprovalMode: ApprovalModeInherit}
	if policy, _ = svc.EffectivePolicy(context.Background(), 1); !policy.Unlimited || policy.AutoApprove {
		t.Fatalf("account limit: %+v, want unlimited with the group's manual approval", policy)
	}

	// A group that inherits leaves the server's settings in place.
	store.limit = nil
	store.groupLimits[1] = &GroupLimit{GroupID: 1, LimitMode: LimitModeInherit, ApprovalMode: ApprovalModeInherit}
	if policy, _ = svc.EffectivePolicy(context.Background(), 1); policy.MaxRequests != 5 || policy.WindowDays != 7 || !policy.AutoApprove {
		t.Fatalf("inheriting group: %+v, want the server's 5 per 7 days, automatic", policy)
	}
}

func TestAdminsIgnoreTheirGroupLimits(t *testing.T) {
	store := newFakeStore()
	store.groupLimits = map[int64]*GroupLimit{1: {GroupID: 1, LimitMode: LimitModeCustom, MaxRequests: intPtr(1), WindowDays: intPtr(1), ApprovalMode: ApprovalModeManual}}
	groupID := int64(1)
	svc := groupedService(store, &models.User{ID: 1, Role: models.RoleAdmin, AccessGroupID: &groupID})

	policy, err := svc.EffectivePolicy(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if policy.MaxRequests != 5 {
		t.Fatalf("admin policy = %+v, want the server's limit: admins are never capped by a group", policy)
	}
}

// Every way to block shows up the same way: a detail page that says blocked.
func TestAccountSwitchBlocksRequestState(t *testing.T) {
	store := newFakeStore()
	groupID := int64(1)
	off := false
	svc := groupedService(store, &models.User{ID: 1, AccessGroupID: &groupID, RequestsAllowed: &off})

	policy, err := svc.EffectivePolicy(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Blocked {
		t.Fatalf("policy = %+v, want blocked by the account's requests switch", policy)
	}
	if state := requestStateFor(testViewer(1), policy, false, nil); state.Requestable || state.Reason != "blocked" {
		t.Fatalf("state = %+v, want not requestable: blocked", state)
	}
}

func TestGroupLimitAdministration(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	admin := Viewer{UserID: 1, ProfileID: "p", IsAdmin: true}

	if _, err := svc.GetGroupLimit(context.Background(), testViewer(1), 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.GetGroupLimit(context.Background(), admin, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown group: err = %v, want ErrNotFound", err)
	}
	fresh, err := svc.GetGroupLimit(context.Background(), admin, 1)
	if err != nil || fresh.LimitMode != LimitModeInherit || fresh.ApprovalMode != ApprovalModeInherit || fresh.Revision != 0 {
		t.Fatalf("unsaved group limit = %+v, %v; want inherit at revision zero", fresh, err)
	}

	var verr *ValidationError
	if _, err := svc.UpsertGroupLimitConditional(context.Background(), admin, GroupLimit{GroupID: 1, LimitMode: LimitModeCustom}, 0); !errors.As(err, &verr) {
		t.Fatalf("custom without numbers: err = %v, want a field error", err)
	}
	for _, blocked := range []GroupLimit{{GroupID: 1, LimitMode: LimitModeBlocked}, {GroupID: 1, ApprovalMode: ApprovalModeBlocked}} {
		if _, err := svc.UpsertGroupLimitConditional(context.Background(), admin, blocked, 0); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%+v: err = %v, want ErrInvalidInput: groups block with their requests switch", blocked, err)
		}
	}
	saved, err := svc.UpsertGroupLimitConditional(context.Background(), admin, GroupLimit{GroupID: 1, LimitMode: LimitModeUnlimited, MaxRequests: intPtr(3), ApprovalMode: ApprovalModeAuto}, 0)
	if err != nil || saved.MaxRequests != nil || saved.ApprovalMode != ApprovalModeAuto {
		t.Fatalf("saved = %+v, %v; want unlimited with no count, automatic", saved, err)
	}
	if _, err := svc.UpsertGroupLimitConditional(context.Background(), admin, GroupLimit{GroupID: 1}, 0); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale save: err = %v, want ErrStaleRevision", err)
	}
}

func TestGroupSwitchAndLegacyBlockBothBlock(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	svc.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: false}})
	if policy, err := svc.EffectivePolicy(context.Background(), 1); err != nil || !policy.Blocked {
		t.Fatalf("group switch off: %+v, %v; want blocked", policy, err)
	}

	store.groupLimits = map[int64]*GroupLimit{1: {GroupID: 1, LimitMode: LimitModeUnlimited, ApprovalMode: ApprovalModeAuto}}
	store.limit = &UserLimit{UserID: 1, LimitMode: LimitModeBlocked, ApprovalMode: ApprovalModeInherit}
	svc = groupedService(store, nil)
	if policy, err := svc.EffectivePolicy(context.Background(), 1); err != nil || !policy.Blocked {
		t.Fatalf("legacy blocked account over an unlimited group: %+v, %v; want blocked", policy, err)
	}
}
