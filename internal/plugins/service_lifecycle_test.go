package plugins

import (
	"context"
	"testing"
)

func TestServiceLifecycleChangeContinuesAfterHookPanic(t *testing.T) {
	var service Service
	called := false
	service.AddLifecycleHook(func(context.Context) {
		panic("boom")
	})
	service.AddLifecycleHook(func(context.Context) {
		called = true
	})

	service.OnLifecycleChange(context.Background())

	if !called {
		t.Fatal("expected later lifecycle hook to run after earlier hook panicked")
	}
}
