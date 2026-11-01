package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/backup"
)

type fakeProfileTransfer struct {
	exports []string
	imports []backup.ProfileImportScope
}

func (f *fakeProfileTransfer) Capability() backup.ProfileExportCapability {
	return backup.ProfileExportCapability{Format: backup.ProfileExportFormat, Version: 1, MaxImportBytes: 10, DataTypes: []string{"ratings"}}
}

func (f *fakeProfileTransfer) Export(_ context.Context, userID int, profileID string) ([]byte, error) {
	f.exports = append(f.exports, profileID)
	return []byte(`{"format":"silo-profile-export","version":1}`), nil
}

func (f *fakeProfileTransfer) Import(_ context.Context, scope backup.ProfileImportScope, _ []byte) (*backup.ProfileImportResult, error) {
	f.imports = append(f.imports, scope)
	return &backup.ProfileImportResult{}, nil
}

// TestProfileTransferActsOnTheVerifiedProfile: export and import read and
// write only the profile the session declared and verified, whatever the
// document names. A PIN-protected profile reached without its PIN (an API
// key is exempt from the gate's PIN check) cannot import.
func TestProfileTransferActsOnTheVerifiedProfile(t *testing.T) {
	f := &fakeProfileTransfer{}
	deps := requestDeps(fixtureRequests())
	deps.ProfileTransfer = f
	h := NewHandler(deps)
	doc := `{"format":"silo-profile-export","version":1,"profile_name":"p-kid","data":{},"items":[]}`

	if rec := do(t, h, http.MethodGet, Prefix+"/profile/export", "", with(bearer(memberToken), "X-Profile-Id", "p-owner")); rec.Code != http.StatusOK {
		t.Fatalf("export = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, Prefix+"/profile/import", doc, with(bearer(memberToken), "X-Profile-Id", "p-owner")); rec.Code != http.StatusOK {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	if len(f.exports) != 1 || f.exports[0] != "p-owner" || len(f.imports) != 1 || f.imports[0].ProfileID != "p-owner" || f.imports[0].UserID == 0 {
		t.Fatalf("exports %v imports %+v, want the session's p-owner", f.exports, f.imports)
	}

	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/profile/import", doc, with(bearer(apiKeyToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	if len(f.imports) != 1 {
		t.Fatalf("a refused import reached the service: %+v", f.imports)
	}
}
