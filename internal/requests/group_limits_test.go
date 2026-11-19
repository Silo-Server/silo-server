package requests

import (
	"context"
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
