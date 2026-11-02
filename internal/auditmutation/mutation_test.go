package auditmutation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A client may percent-encode a token's characters. Chi then routes on
// URL.RawPath and stores the escaped segment, while log sinks pass the
// decoded URL.Path; both spellings must be redacted.
func TestRedactSecretPathPercentEncodedToken(t *testing.T) {
	var got string
	router := chi.NewRouter()
	router.Post("/api/v2/password-resets/{token}/complete", func(_ http.ResponseWriter, r *http.Request) {
		got = RedactSecretPath(chi.RouteContext(r.Context()), r.URL.Path)
	})
	// %32 is "2": the token 2riBxLS4... with its first character escaped.
	target := "/api/v2/password-resets/%32riBxLS4UcC8w8WdRoge8F0f8LOLkz4yPsPNA01gWBY/complete"
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, target, nil))
	if want := "/api/v2/password-resets/[redacted]/complete"; got != want {
		t.Fatalf("redacted path = %q, want %q", got, want)
	}
}

func TestUserChangeDetailsNeverIncludePasswordHash(t *testing.T) {
	before := &models.User{ID: 2, Permissions: []string{"marker_edit"}, PasswordHash: "secret-canary"}
	after := &models.User{ID: 2, Permissions: []string{}, PasswordHash: "new-secret-canary", Enabled: true}
	changes := Changes(UserValues(before), UserValues(after))
	raw, _ := json.Marshal(changes)
	if strings.Contains(string(raw), "canary") {
		t.Fatal("secret in audit")
	}
	if len(changes) != 2 || changes[1].Field != "permissions" || *changes[1].Before != `["marker_edit"]` || *changes[1].After != `[]` {
		t.Fatalf("changes: %s", raw)
	}
}

func TestNullablePolicyListsRetainMeaningfulEmptyTransitions(t *testing.T) {
	changes := Changes(map[string]any{auditFieldLibraryIDs: []int(nil), "allowed_permissions": []string(nil)},
		map[string]any{auditFieldLibraryIDs: []int{}, "allowed_permissions": []string{}})
	if len(changes) != 2 {
		t.Fatalf("unrestricted-to-empty policy changes lost: %+v", changes)
	}
	for _, change := range changes {
		if *change.Before != "null" || *change.After != "[]" {
			t.Fatalf("lost policy meaning: %+v", change)
		}
	}
}
