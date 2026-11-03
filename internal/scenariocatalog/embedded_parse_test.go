package scenariocatalog

import "testing"

func TestOperationClassFromEmbeddedDocument(t *testing.T) {
	for id, want := range map[string]string{
		"getSetupStatus":          "public",
		"listPersonalAPIKeys":     "authenticated",
		"getAdminSystemResources": "acting_admin",
		"getProfileSectionFlags":  "profile_scoped",
	} {
		got, ok := OperationClass(id)
		if !ok || got != want {
			t.Fatalf("%s class = %q (%v), want %q", id, got, ok, want)
		}
	}
	if _, ok := OperationClass("noSuchOperation"); ok {
		t.Fatal("unknown operation must not report a class")
	}
}
