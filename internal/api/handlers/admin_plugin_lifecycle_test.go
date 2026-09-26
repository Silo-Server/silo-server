package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// The lifecycle seams refuse unwired stores with a 503 APIError and shape
// refusals with field errors, before any network or store call.
func TestPluginLifecycleSeamRefusals(t *testing.T) {
	t.Parallel()
	var apiErr *APIError
	unwired := &PluginHandler{}
	if _, err := unwired.CreateAdminPluginInstallation(t.Context(), PluginInstallationCreateInput{ArchiveURL: "https://example.invalid/a.zip"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("create err = %v", err)
	}
	if _, err := unwired.UpdateAdminPluginInstallation(t.Context(), 1, PluginInstallationUpdateInput{}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("update err = %v", err)
	}
	if _, err := unwired.ApplyAdminPluginUpdate(t.Context(), 1); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("apply err = %v", err)
	}
	if _, err := unwired.RestartAdminPluginInstallation(t.Context(), 1); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("restart err = %v", err)
	}
	if err := unwired.DeleteAdminPluginInstallation(t.Context(), 1); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("delete err = %v", err)
	}
	var nilHandler *PluginHandler
	if err := nilHandler.DeleteAdminPluginInstallation(t.Context(), 1); !errors.As(err, &apiErr) {
		t.Fatalf("nil handler err = %v", err)
	}
	// Shape refusals need a wired service but never reach it: an empty
	// *plugins.Service is enough because the refusal happens first.
	shaped := &PluginHandler{installations: &plugins.InstallationStore{}, service: &plugins.Service{}}
	for _, tc := range []struct {
		in    PluginInstallationCreateInput
		field string
	}{
		{PluginInstallationCreateInput{}, "archive_url"},
		{PluginInstallationCreateInput{PluginID: "org.example.a"}, "plugin_id"},
		{PluginInstallationCreateInput{RepositoryID: new(4), PluginID: "org.example.a", Version: "1", ArchiveURL: "https://example.invalid/a.zip"}, "archive_url"},
	} {
		_, err := shaped.CreateAdminPluginInstallation(t.Context(), tc.in)
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Field != tc.field {
			t.Fatalf("%+v: err = %v, want 400 at %s", tc.in, err, tc.field)
		}
	}
}
