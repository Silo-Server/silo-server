package workercontrolcore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type unavailableStatusNodes struct{ NodeRegistry }

func (n unavailableStatusNodes) ByID(context.Context, int) (Node, bool, error) {
	return Node{}, false, ErrUnavailable
}
func TestWorkerProofDependencyErrorRemainsUnavailable(t *testing.T) {
	s, _, node := authorityTestService(t)
	s.options.Nodes = unavailableStatusNodes{s.options.Nodes}
	r := httptest.NewRequest("POST", "https://core.example/api/internal/workers/7/sources/grant/renew", strings.NewReader(`{"work_id":"live","generation":1}`))
	if e := s.VerifyRequest(r, node.ID, workeridentity.DomainNodeProof); !errors.Is(e, ErrUnavailable) {
		t.Errorf("registry unavailable misclassified: %v", e)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("dependency proof HTTP%d headers=%v", w.Code, w.Header())
	}
}

func TestWorkActiveStatusUnknownDoesNotCacheDenial(t *testing.T) {
	service, store, node := authorityTestService(t)
	assignment := activateAuthorityWork(t, store, "live")
	var unavailable atomic.Bool
	unavailable.Store(true)
	service.options.ExecutionActive = func(context.Context, Node, workerwire.Assignment) bool { return false }
	service.options.ExecutionStatus = func(context.Context, Node, workerwire.Assignment) (bool, error) {
		if unavailable.Load() {
			return false, ErrUnavailable
		}
		return true, nil
	}
	if service.WorkActive(t.Context(), node.ID, assignment.WorkID, assignment.Generation) {
		t.Fatal("compatibility bool changed")
	}
	active, err := service.WorkActiveStatus(t.Context(), node.ID, assignment.WorkID, assignment.Generation)
	if active || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown=%v,%v", active, err)
	}
	unavailable.Store(false)
	active, err = service.WorkActiveStatus(t.Context(), node.ID, assignment.WorkID, assignment.Generation)
	if !active || err != nil {
		t.Fatalf("unknown/compatibility denial poisoned recovery=%v,%v", active, err)
	}
	if err = store.Stop(t.Context(), node.ID, assignment.WorkID); err != nil {
		t.Fatal(err)
	}
	service.invalidateCached(node.ID, assignment.WorkID)
	active, err = service.WorkActiveStatus(t.Context(), node.ID, assignment.WorkID, assignment.Generation)
	if active || err != nil {
		t.Fatalf("definite stop=%v,%v", active, err)
	}
}
