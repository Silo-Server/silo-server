package workercontrolcore

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestOutputSnapshotSharesOnlyRequestLocalFactsWithBatchExecution(t *testing.T) {
	f := newOutputSnapshotFixture(t)
	first := f.record(t, f.assignment(t, "first", true), "first-viewer")
	second := f.record(t, f.assignment(t, "second", true), "second-viewer")
	type contextKey struct{}
	var previous, current *int
	var calls int
	f.service.options.SnapshotContext = func(ctx context.Context) context.Context {
		current = new(int)
		if previous == current {
			t.Fatal("snapshot reused facts from the preceding request")
		}
		previous = current
		return context.WithValue(ctx, contextKey{}, current)
	}
	f.service.options.ExecutionStatus = func(context.Context, Node, workerwire.Assignment) (bool, error) {
		t.Fatal("batch snapshot fell back to per-work execution checks")
		return false, nil
	}
	f.service.options.ExecutionStatuses = func(ctx context.Context, node Node, assignments []workerwire.Assignment) ([]bool, error) {
		calls++
		if ctx.Value(contextKey{}) != current || node.ID != 7 || len(assignments) != 2 {
			t.Fatal("batch lost request facts or the immutable assignment snapshot")
		}
		allowed := make([]bool, len(assignments))
		for i, assignment := range assignments {
			allowed[i] = assignment.WorkID == "second"
		}
		return allowed, nil
	}
	verify := f.service.options.VerifyTokens
	f.service.options.VerifyTokens = func(ctx context.Context, tokens []string) ([]TokenVerification, error) {
		if ctx.Value(contextKey{}) != current {
			t.Fatal("viewer verification did not share execution's request facts")
		}
		return verify(ctx, tokens)
	}
	for range 2 {
		snapshot := f.snapshot(t, 200)
		if snapshot.Outputs[first] || !snapshot.Outputs[second] || len(snapshot.Active) != 1 || snapshot.Active["second"] == 0 {
			t.Fatal("batch execution results changed assignment/result correspondence")
		}
	}
	if calls != 2 {
		t.Fatal("snapshot repeated or cached the execution batch across requests")
	}
	for _, failure := range []string{"short", "unknown", "nil-context"} {
		t.Run(failure, func(t *testing.T) {
			switch failure {
			case "short":
				f.service.options.ExecutionStatuses = func(context.Context, Node, []workerwire.Assignment) ([]bool, error) {
					return []bool{true}, nil
				}
			case "unknown":
				f.service.options.ExecutionStatuses = func(context.Context, Node, []workerwire.Assignment) ([]bool, error) {
					return nil, errors.New("dependency unavailable")
				}
			case "nil-context":
				f.service.options.SnapshotContext = func(context.Context) context.Context { return nil }
			}
			f.snapshot(t, 503)
			if records, err := f.registry.List(t.Context(), 7); err != nil || len(records) != 2 {
				t.Fatal("incomplete batch retired recoverable viewer records")
			}
		})
	}
}

func TestWorkActiveStatusDoesNotBypassBatchExecution(t *testing.T) {
	f := newOutputSnapshotFixture(t)
	a := f.assignment(t, "work", true)
	f.service.options.ExecutionStatus = nil
	f.service.options.ExecutionStatuses = func(context.Context, Node, []workerwire.Assignment) ([]bool, error) {
		return []bool{false}, nil
	}
	active, err := f.service.WorkActiveStatus(t.Context(), 7, a.WorkID, a.Generation)
	if err != nil || active {
		t.Fatalf("single-work status bypassed the configured execution batch: active=%v err=%v", active, err)
	}
}
