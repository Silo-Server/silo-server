package apiv2

import (
	"reflect"
	"testing"
)

func TestGuardedReceiptRejectsAmbiguousResponseShape(t *testing.T) {
	op := Operation{Guarded: true, GuardedReceipt: true}
	op.Method = "PATCH"
	input := reflect.TypeFor[AdminRateLimitUpdateInput]()
	if err := checkConcurrencyShape(op, input, reflect.TypeFor[AdminRateLimitUpdateOutput]()); err != nil {
		t.Fatalf("valid receipt: %v", err)
	}
	for _, output := range []reflect.Type{
		reflect.TypeFor[struct{}](),
		reflect.TypeFor[struct {
			ETag string `header:"ETag"`
			Body AdminRateLimitUpdateResult
		}](),
	} {
		if err := checkConcurrencyShape(op, input, output); err == nil {
			t.Fatalf("invalid receipt shape accepted: %v", output)
		}
	}
}
