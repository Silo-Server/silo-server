package nodepool

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Source access is stored for transcode nodes, defaults to local, moves the
// admin revision when it changes, and is refused for a proxy on create and
// update without writing anything.
func TestAdminConfigurationStoreSourceAccess(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	store := NewAdminConfigurationStore(pool)
	repo := NewRepository(pool)
	suffix := fmt.Sprint(time.Now().UnixNano())
	create := func(input CreateNodeInput) *Node {
		t.Helper()
		node, err := store.Create(ctx, input)
		if err != nil {
			t.Fatalf("create %s: %v", input.Name, err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), node.ID) })
		return node
	}
	noGuard := func(int64) error { return nil }

	plain := create(CreateNodeInput{Name: "sa-default-" + suffix, Type: NodeTypeTranscode, URL: "http://sa-default-" + suffix})
	if plain.SourceAccess != SourceAccessLocal {
		t.Fatalf("default source_access = %q, want local", plain.SourceAccess)
	}
	thin := create(CreateNodeInput{Name: "sa-core-" + suffix, Type: NodeTypeTranscode, URL: "http://sa-core-" + suffix, SourceAccess: SourceAccessCore})
	if thin.SourceAccess != SourceAccessCore {
		t.Fatalf("created source_access = %q, want core", thin.SourceAccess)
	}
	// An identical create resolves to the same row; a different source access
	// is a conflicting configuration.
	if again, err := store.Create(ctx, CreateNodeInput{Name: thin.Name, Type: NodeTypeTranscode, URL: thin.URL, SourceAccess: SourceAccessCore}); err != nil || again.ID != thin.ID {
		t.Fatalf("repeated create = %v, %v", again, err)
	}
	if _, err := store.Create(ctx, CreateNodeInput{Name: thin.Name, Type: NodeTypeTranscode, URL: thin.URL}); !errors.Is(err, ErrNodeConfigurationConflict) {
		t.Fatalf("conflicting create = %v, want ErrNodeConfigurationConflict", err)
	}

	local := SourceAccessLocal
	updated, previous, err := store.Update(ctx, thin.ID, UpdateNodeInput{SourceAccess: &local}, noGuard)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SourceAccess != SourceAccessLocal || previous.SourceAccess != SourceAccessCore {
		t.Fatalf("update: %q -> %q", previous.SourceAccess, updated.SourceAccess)
	}
	if updated.AdminRevision == previous.AdminRevision {
		t.Fatal("a source access change did not move the admin revision")
	}
	bad := "nfs"
	if _, _, err := store.Update(ctx, thin.ID, UpdateNodeInput{SourceAccess: &bad}, noGuard); !errors.Is(err, ErrInvalidNodeInput) {
		t.Fatalf("unknown value = %v, want ErrInvalidNodeInput", err)
	}

	if _, err := store.Create(ctx, CreateNodeInput{Name: "sa-proxy-bad-" + suffix, Type: NodeTypeProxy, URL: "http://sa-proxy-bad-" + suffix, SourceAccess: SourceAccessCore}); !errors.Is(err, ErrInvalidNodeInput) {
		t.Fatalf("proxy create = %v, want ErrInvalidNodeInput", err)
	}
	proxy := create(CreateNodeInput{Name: "sa-proxy-" + suffix, Type: NodeTypeProxy, URL: "http://sa-proxy-" + suffix})
	core := SourceAccessCore
	name := "renamed-" + suffix
	if _, _, err := store.Update(ctx, proxy.ID, UpdateNodeInput{Name: &name, SourceAccess: &core}, noGuard); !errors.Is(err, ErrInvalidNodeInput) {
		t.Fatalf("proxy update = %v, want ErrInvalidNodeInput", err)
	}
	if read, err := repo.GetByID(ctx, proxy.ID); err != nil || read.Name != proxy.Name {
		t.Fatalf("a refused proxy update wrote: %v, %v", read, err)
	}
}
