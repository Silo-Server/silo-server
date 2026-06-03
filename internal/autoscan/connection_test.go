package autoscan

import (
	"context"
	"errors"
	"testing"
)

// fakeRequestLookup implements RequestIntegrationLookup from a static map.
type fakeRequestLookup struct {
	entries map[string]struct{ baseURL, apiKeyRef string }
	err     error
}

func (f fakeRequestLookup) Get(_ context.Context, id string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	e, ok := f.entries[id]
	if !ok {
		return "", "", errors.New("integration not found: " + id)
	}
	return e.baseURL, e.apiKeyRef, nil
}

// fakeSecrets implements SecretResolver from a static map; unknown refs resolve
// to empty (so the ref itself is used).
type fakeSecrets struct {
	values map[string]string
	err    error
}

func (f fakeSecrets) Get(_ context.Context, ref string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.values[ref], nil
}

func TestResolveConnectionOwnCredentials(t *testing.T) {
	secrets := fakeSecrets{values: map[string]string{"ownref": "OWNKEY"}}
	r := NewConnectionResolver(fakeRequestLookup{}, secrets)

	got, err := r.Resolve(context.Background(), Connection{
		BaseURL:   "http://own:8989",
		APIKeyRef: "ownref",
	})
	if err != nil {
		t.Fatalf("Resolve own: %v", err)
	}
	if got.BaseURL != "http://own:8989" || got.APIKey != "OWNKEY" {
		t.Fatalf("own resolve = %+v", got)
	}
}

func TestResolveConnectionLinked(t *testing.T) {
	lookup := fakeRequestLookup{entries: map[string]struct{ baseURL, apiKeyRef string }{
		"req-1": {baseURL: "http://req:7878", apiKeyRef: "rk"},
	}}
	secrets := fakeSecrets{values: map[string]string{"rk": "REQKEY"}}
	r := NewConnectionResolver(lookup, secrets)

	id := "req-1"
	got, err := r.Resolve(context.Background(), Connection{RequestIntegrationID: &id})
	if err != nil {
		t.Fatalf("Resolve linked: %v", err)
	}
	if got.BaseURL != "http://req:7878" || got.APIKey != "REQKEY" {
		t.Fatalf("linked resolve = %+v", got)
	}
}

func TestResolveConnectionLinkedMissingErrors(t *testing.T) {
	r := NewConnectionResolver(fakeRequestLookup{}, fakeSecrets{})
	id := "gone"
	if _, err := r.Resolve(context.Background(), Connection{RequestIntegrationID: &id}); err == nil {
		t.Fatal("expected error when linked Requests integration is missing")
	}
}

func TestResolveConnectionLinkedLookupErrors(t *testing.T) {
	r := NewConnectionResolver(fakeRequestLookup{err: errors.New("boom")}, fakeSecrets{})
	id := "req-1"
	if _, err := r.Resolve(context.Background(), Connection{RequestIntegrationID: &id}); err == nil {
		t.Fatal("expected error when the lookup itself fails")
	}
}

func TestResolveConnectionEmptyIntegrationIDIsNotALink(t *testing.T) {
	// A pointer-to-empty/whitespace request_integration_id must NOT be treated as
	// a live link: it must fall back to the connection's own fields, never call
	// requests.Get(""). The lookup here errors on any call, so reaching it fails.
	r := NewConnectionResolver(
		fakeRequestLookup{err: errors.New("lookup must not be called")},
		fakeSecrets{values: map[string]string{"ownref": "OWNKEY"}},
	)
	for _, empty := range []string{"", "   "} {
		id := empty
		got, err := r.Resolve(context.Background(), Connection{
			BaseURL:              "http://own:8989",
			APIKeyRef:            "ownref",
			RequestIntegrationID: &id,
		})
		if err != nil {
			t.Fatalf("Resolve with empty integration id %q: %v", empty, err)
		}
		if got.BaseURL != "http://own:8989" || got.APIKey != "OWNKEY" {
			t.Fatalf("empty integration id %q should use own creds, got %+v", empty, got)
		}
	}
}

func TestResolveConnectionTrimsAndFallsBackOnEmptySecret(t *testing.T) {
	// Whitespace-padded ref; secret resolves to whitespace-only -> fall back to
	// the trimmed ref (parity with requests.resolveAPIKey).
	secrets := fakeSecrets{values: map[string]string{"ownref": "   "}}
	r := NewConnectionResolver(fakeRequestLookup{}, secrets)

	got, err := r.Resolve(context.Background(), Connection{
		BaseURL:   "http://own:8989",
		APIKeyRef: "  ownref  ",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.APIKey != "ownref" {
		t.Fatalf("expected trimmed ref fallback, got %q", got.APIKey)
	}
}
