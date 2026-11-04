package plugins

import (
	"reflect"
	"testing"
)

func TestDeriveRequiredPermissions(t *testing.T) {
	known := []string{"admin:monitor:read", "admin:sessions:control", "libraries:read"}
	tests := []struct {
		name         string
		capabilities []*Capability
		scopes       []string
		want         RequiredPermissions
		grant        *PermissionGrant
		wantPending  bool
	}{
		{
			name:         "public and plugin events ask for nothing",
			capabilities: []*Capability{eventConsumer(1, "events", "catalog.library.changed", "catalog.item.changed", "plugin.silo.other.ping")},
			want:         RequiredPermissions{EventAccess: []string{}, RequestedScopes: []string{}, RequiredScopes: []string{}, UnsupportedScopes: []string{}},
			wantPending:  false,
		},
		{
			name: "admin and user_data classes, sorted and unique across capabilities",
			capabilities: []*Capability{
				eventConsumer(1, "a", "notification.created", "session.started"),
				eventConsumer(1, "b", "scan.completed", "session.stopped"),
			},
			want:        RequiredPermissions{EventAccess: []string{"admin", "user_data"}, RequestedScopes: []string{}, RequiredScopes: []string{}, UnsupportedScopes: []string{}},
			grant:       &PermissionGrant{ApprovedEventAccess: []string{"admin"}},
			wantPending: true,
		},
		{
			name:         "an installation without a grant is pending for admin events",
			capabilities: []*Capability{eventConsumer(1, "events", "job.failed")},
			want:         RequiredPermissions{EventAccess: []string{"admin"}, RequestedScopes: []string{}, RequiredScopes: []string{}, UnsupportedScopes: []string{}},
			wantPending:  true,
		},
		{
			name: "names outside the catalog and other capability types ask for nothing",
			capabilities: []*Capability{
				eventConsumer(1, "events", "user_state.changed", "library.scan.completed"),
				{Type: "scheduled_task.v1", ID: "sync", Metadata: map[string]any{"subscriptions": []any{"session.started"}}},
			},
			want: RequiredPermissions{EventAccess: []string{}, RequestedScopes: []string{}, RequiredScopes: []string{}, UnsupportedScopes: []string{}},
		},
		{
			name:         "unknown scopes are reported and never required",
			capabilities: []*Capability{eventConsumer(1, "events", "session.started")},
			scopes:       []string{"admin:monitor:read", "admin:everything", "admin:monitor:read", "libraries:read"},
			want: RequiredPermissions{
				EventAccess:       []string{"admin"},
				RequestedScopes:   []string{"admin:everything", "admin:monitor:read", "libraries:read"},
				RequiredScopes:    []string{"admin:monitor:read", "libraries:read"},
				UnsupportedScopes: []string{"admin:everything"},
			},
			grant: &PermissionGrant{ApprovedEventAccess: []string{"admin"}, ApprovedHostAPIScopes: []string{"admin:monitor:read", "libraries:read"},
				APIKeyID: new(int64(9)), keyOwnerAdmin: true},
			wantPending: false,
		},
		{
			name:   "approved scopes whose key's owner is no longer an enabled admin are pending again",
			scopes: []string{"admin:monitor:read"},
			want:   RequiredPermissions{EventAccess: []string{}, RequestedScopes: []string{"admin:monitor:read"}, RequiredScopes: []string{"admin:monitor:read"}, UnsupportedScopes: []string{}},
			grant: &PermissionGrant{ApprovedHostAPIScopes: []string{"admin:monitor:read"},
				APIKeyID: new(int64(9)), keyOwnerAdmin: false},
			wantPending: true,
		},
		{
			name:        "approved scopes whose key was revoked are pending again",
			scopes:      []string{"admin:monitor:read"},
			want:        RequiredPermissions{EventAccess: []string{}, RequestedScopes: []string{"admin:monitor:read"}, RequiredScopes: []string{"admin:monitor:read"}, UnsupportedScopes: []string{}},
			grant:       &PermissionGrant{ApprovedHostAPIScopes: []string{"admin:monitor:read"}},
			wantPending: true,
		},
		{
			name:        "only unknown scopes leave nothing pending",
			scopes:      []string{"admin:everything"},
			want:        RequiredPermissions{EventAccess: []string{}, RequestedScopes: []string{"admin:everything"}, RequiredScopes: []string{}, UnsupportedScopes: []string{"admin:everything"}},
			wantPending: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveRequiredPermissions(tt.capabilities, tt.scopes, known)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("DeriveRequiredPermissions = %+v, want %+v", got, tt.want)
			}
			if pending := PermissionsPending(got, tt.grant); pending != tt.wantPending {
				t.Fatalf("PermissionsPending = %v, want %v", pending, tt.wantPending)
			}
		})
	}
}
