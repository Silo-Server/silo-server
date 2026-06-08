package autoscan

import (
	"context"
	"errors"
	"testing"
)

// fakeRequestLookup implements RequestIntegrationLookup from a static map. The
// returned apiKey is already plaintext (the Requests repo decrypts on read).
type fakeRequestLookup struct {
	entries map[string]struct{ baseURL, apiKey string }
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
	return e.baseURL, e.apiKey, nil
}

func TestResolveConnectionOwnCredentials(t *testing.T) {
	// The autoscan repo already decrypted api_key_ref, so the connection carries
	// the literal key; Resolve returns it verbatim (trimmed).
	r := NewConnectionResolver(fakeRequestLookup{})

	got, err := r.Resolve(context.Background(), Connection{
		BaseURL:   "http://own:8989",
		APIKeyRef: "OWNKEY",
	})
	if err != nil {
		t.Fatalf("Resolve own: %v", err)
	}
	if got.BaseURL != "http://own:8989" || got.APIKey != "OWNKEY" {
		t.Fatalf("own resolve = %+v", got)
	}
}

func TestResolveConnectionLinked(t *testing.T) {
	lookup := fakeRequestLookup{entries: map[string]struct{ baseURL, apiKey string }{
		"req-1": {baseURL: "http://req:7878", apiKey: "REQKEY"},
	}}
	r := NewConnectionResolver(lookup)

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
	r := NewConnectionResolver(fakeRequestLookup{})
	id := "gone"
	if _, err := r.Resolve(context.Background(), Connection{RequestIntegrationID: &id}); err == nil {
		t.Fatal("expected error when linked Requests integration is missing")
	}
}

func TestResolveConnectionLinkedLookupErrors(t *testing.T) {
	r := NewConnectionResolver(fakeRequestLookup{err: errors.New("boom")})
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
	)
	for _, empty := range []string{"", "   "} {
		id := empty
		got, err := r.Resolve(context.Background(), Connection{
			BaseURL:              "http://own:8989",
			APIKeyRef:            "OWNKEY",
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

func TestResolveConnectionTrimsKey(t *testing.T) {
	// Resolve trims surrounding whitespace from the (decrypted) key.
	r := NewConnectionResolver(fakeRequestLookup{})

	got, err := r.Resolve(context.Background(), Connection{
		BaseURL:   "http://own:8989",
		APIKeyRef: "  OWNKEY  ",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.APIKey != "OWNKEY" {
		t.Fatalf("expected trimmed key, got %q", got.APIKey)
	}
}
