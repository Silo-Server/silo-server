package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

type recipeClassificationRegistry struct {
	node       Node
	registered bool
	err        error
}

func (r recipeClassificationRegistry) ByURL(context.Context, string) (Node, bool, error) {
	return r.node, r.registered, r.err
}

func (r recipeClassificationRegistry) ByID(context.Context, int) (Node, bool, error) {
	return r.node, r.registered, r.err
}

func TestRecipeClassificationUsesRegisteredNodeAuthority(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	for _, scenario := range []struct {
		name, target, stored string
		registered, enabled  bool
		allowHTTP, weakKey   bool
		lookupError          bool
		required, failure    bool
	}{
		{name: "legacy-prefix", target: "http://legacy.example/prefix", enabled: true},
		{name: "registered", target: "https://worker.example", stored: "https://worker.example", registered: true, enabled: true, required: true},
		{name: "canonical", target: "HTTPS://WORKER.example:00443/", stored: "https://worker.example/", registered: true, enabled: true, required: true},
		{name: "explicit-http", target: "http://worker.example", stored: "http://worker.example", registered: true, enabled: true, allowHTTP: true, required: true},
		{name: "http-without-policy", target: "http://worker.example", stored: "http://worker.example", registered: true, enabled: true, failure: true},
		{name: "disabled", target: "https://worker.example", stored: "https://worker.example", registered: true, failure: true},
		{name: "edited-prefix", target: "https://worker.example", stored: "https://worker.example/prefix", registered: true, enabled: true, failure: true},
		{name: "edited-query", target: "https://worker.example", stored: "https://worker.example?edited=1", registered: true, enabled: true, failure: true},
		{name: "edited-fragment", target: "https://worker.example", stored: "https://worker.example#edited", registered: true, enabled: true, failure: true},
		{name: "request-prefix", target: "https://worker.example/prefix", stored: "https://worker.example", registered: true, enabled: true, failure: true},
		{name: "request-query", target: "https://worker.example?edited=1", stored: "https://worker.example", registered: true, enabled: true, failure: true},
		{name: "request-userinfo", target: "https://user@worker.example", stored: "https://worker.example", registered: true, enabled: true, failure: true},
		{name: "weak-key", target: "https://worker.example", stored: "https://worker.example", registered: true, enabled: true, weakKey: true, failure: true},
		{name: "registry-error", target: "https://worker.example", lookupError: true, failure: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			key := public
			if scenario.weakKey {
				key = nil
			}
			registry := recipeClassificationRegistry{node: Node{ID: 17, URL: scenario.stored, PublicKey: key, Enabled: scenario.enabled, AllowHTTP: scenario.allowHTTP}, registered: scenario.registered}
			if scenario.lookupError {
				registry.err = errors.New("registry unavailable")
			}
			service, err := New(Options{Origin: "https://core.example", PrivateKey: private, Nodes: registry, Store: NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			classifier, available := any(service).(interface {
				RequiresRecipe(context.Context, string) (bool, error)
			})
			if !available {
				t.Fatal("core cannot classify registered standalone recipe authority")
			}
			required, err := classifier.RequiresRecipe(t.Context(), scenario.target)
			if required != scenario.required || (err != nil) != scenario.failure {
				t.Fatalf("classification requires=%v err=%v", required, err)
			}
		})
	}
}
