package apiv2

import (
	"reflect"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

func TestGuardedReceiptDocumentsOnlyConflictValidator(t *testing.T) {
	api := huma.NewAPI(humaConfig(), noopAdapter{})
	reg := &Registry{api: api}
	registerAdminRateLimitWrite(reg)
	op := api.OpenAPI().Paths[Prefix+"/admin/rate-limits/config"].Patch
	if op.Responses["200"].Headers[etagField] != nil {
		t.Fatal("receipt success advertises a validator it does not return")
	}
	if op.Responses["412"].Headers[etagField] == nil || op.Metadata[metaIdentityOnly] != true {
		t.Fatal("receipt lost conflict validator or encoding guard")
	}
}

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
