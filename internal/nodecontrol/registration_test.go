package nodecontrol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// declaringNodes applies a declaration by answering with err, or by recording
// which node declared what.
type declaringNodes struct {
	err      error
	declared map[int]nodepool.UpdateNodeInput
}

func (d *declaringNodes) DeclareNode(_ context.Context, id int, input nodepool.UpdateNodeInput) (*nodepool.Node, bool, error) {
	if d.err != nil {
		return nil, false, d.err
	}
	d.declared[id] = input
	return &nodepool.Node{ID: id, Name: "gpu-7", URL: "http://gpu-7:8080", EnvManaged: input.SetFields()}, true, nil
}

// A registration applies to the node whose key signed it, and the node's
// client reads each answer the way its startup rules need: the result, a
// refusal naming its field, or a status for a core without the route.
func TestRegistrationOverTheWire(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	otherKey := f.addNode(8, "gpu-8")
	registrar := &declaringNodes{declared: map[int]nodepool.UpdateNodeInput{}}
	f.server.opts.Registrar = registrar
	core := httptest.NewServer(f.handler)
	t.Cleanup(core.Close)
	client := func(key string) *Client {
		c, err := NewClient(core.URL, key)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	group := "rack-a"

	if got := f.serve(7, otherKey, http.MethodPut, PathRegistration, Registration{Group: &group}, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("signed with another node's key: %d", got.Code)
	}
	result, err := client(key).Register(t.Context(), Registration{Group: &group})
	if err != nil {
		t.Fatal(err)
	}
	if declared, ok := registrar.declared[7]; !ok || declared.Group == nil || *declared.Group != group || len(registrar.declared) != 1 {
		t.Fatalf("declared %+v, want node 7's group only", registrar.declared)
	}
	if result.Node.ID != 7 || len(result.EnvManaged) != 1 || result.EnvManaged[0] != nodepool.FieldGroup {
		t.Fatalf("result %+v", result)
	}
	// A field the core does not know must not read as an empty declaration,
	// which would release the node's locks.
	if got := f.serve(7, key, http.MethodPut, PathRegistration, map[string]int{"max_job": 3}, nil); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field: %d, want 422", got.Code)
	}
	if declared := registrar.declared[7]; declared.Group == nil {
		t.Fatal("an unknown field replaced node 7's declaration")
	}

	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantField  string
	}{
		{"invalid value", &nodepool.DeclarationError{Field: nodepool.FieldHWAccelOverride, Detail: "bad"}, http.StatusUnprocessableEntity, nodepool.FieldHWAccelOverride},
		{"url taken", nodepool.ErrTranscodeNodeURLTaken, http.StatusConflict, nodepool.FieldURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registrar.err = tc.err
			_, err := client(otherKey).Register(t.Context(), Registration{})
			refusal, ok := errors.AsType[*RegistrationError](err)
			if !ok || refusal.Status != tc.wantStatus || refusal.Field != tc.wantField {
				t.Fatalf("err %v, want a %d refusal for %s", err, tc.wantStatus, tc.wantField)
			}
		})
	}

	// Registration reloads every API server's node pools, so one node gets a
	// handful of them and then 429.
	registrar.err = nil
	spendKey := f.addNode(9, "gpu-9")
	last := 0
	for range nodeRegistrationBurst + 1 {
		last = f.serve(9, spendKey, http.MethodPut, PathRegistration, Registration{}, nil).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after the registration budget: %d, want 429", last)
	}

	// A core that predates the route answers 404 from its router.
	f.server.opts.Registrar = nil
	old := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(old.Close)
	oldClient, err := NewClient(old.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldClient.Register(t.Context(), Registration{}); Status(err) != http.StatusNotFound {
		t.Fatalf("older core: %v, want status 404", err)
	}
}
