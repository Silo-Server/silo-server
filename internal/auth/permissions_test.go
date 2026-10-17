package auth

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

func TestNormalizePermissions_DeduplicatesAndSorts(t *testing.T) {
	got, err := NormalizePermissions([]string{
		"marker_edit",
		" metadata_curation ",
		"metadata_curation",
		"",
	})
	if err != nil {
		t.Fatalf("NormalizePermissions returned error: %v", err)
	}
	want := []string{"marker_edit", "metadata_curation"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %#v, want %#v", got, want)
	}
}

func TestNormalizePermissions_RejectsUnknownPermission(t *testing.T) {
	if _, err := NormalizePermissions([]string{"server_owner"}); err == nil {
		t.Fatal("expected unknown permission error")
	}
}

func TestDefaultUserPermissionsIncludesMarkerEditOnly(t *testing.T) {
	got := DefaultUserPermissions()
	want := []string{"marker_edit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default permissions = %#v, want %#v", got, want)
	}
}

func TestPolicyPermissions(t *testing.T) {
	tests := []struct {
		name      string
		effective []string
		want      []string
	}{
		{name: "empty", want: []string{}},
		{name: "masked list passes through", effective: []string{"metadata_curation"}, want: []string{"metadata_curation"}},
		{
			name:      "unknown and duplicate entries dropped",
			effective: []string{"server_owner", "metadata_curation", "marker_edit", "marker_edit"},
			want:      []string{"marker_edit", "metadata_curation"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PolicyPermissions(access.EffectiveUserPolicy{Permissions: tt.effective})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("PolicyPermissions = %#v, want %#v", got, tt.want)
			}
		})
	}
}
