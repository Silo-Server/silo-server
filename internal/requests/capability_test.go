package requests

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRequestCapabilityAllowedUsesAccountAndGroupPolicy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		groupAllowed bool
		override     *bool
		limit        *UserLimit
		want         bool
	}{
		{name: "group denied", groupAllowed: false, want: false},
		{name: "account overrides group", groupAllowed: false, override: new(true), want: true},
		{name: "account denied", groupAllowed: true, override: new(false), want: false},
		{name: "blocked limit", groupAllowed: true, limit: &UserLimit{LimitMode: LimitModeBlocked}, want: false},
		{name: "blocked approval", groupAllowed: true, limit: &UserLimit{ApprovalMode: ApprovalModeBlocked}, want: false},
		{name: "exhausted quota is capacity", groupAllowed: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.limit = tc.limit
			store.count = 100
			store.settings.GlobalMaxRequests = 1
			service := newTestService(store)
			groupID := int64(1)
			service.SetUserRepository(requestUserRepo{user: &models.User{ID: 1, AccessGroupID: &groupID, RequestsAllowed: tc.override}})
			service.SetGroupPolicyProvider(requestGroupProvider{group: &access.GroupPolicy{RequestsAllowed: tc.groupAllowed}})
			got, err := service.RequestCapabilityAllowed(t.Context(), testViewer(1))
			if err != nil || got != tc.want {
				t.Fatalf("allowed=%v err=%v want %v", got, err, tc.want)
			}
		})
	}
}
