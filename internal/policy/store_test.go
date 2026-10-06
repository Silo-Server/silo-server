package policy

import (
	"context"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPolicyStoreDocumentVersionCRUD(t *testing.T) {
	ctx := context.Background()
	_, store := newPolicyStoreTest(t, ctx)

	document, err := store.CreateDocument(ctx, "scope", "household scope")
	if err != nil {
		t.Fatalf("CreateDocument() error: %v", err)
	}
	if document.ID == 0 || document.Domain != "scope" || !document.Enabled {
		t.Fatalf("CreateDocument() = %#v", document)
	}

	gotDocument, err := store.GetDocument(ctx, document.ID)
	if err != nil {
		t.Fatalf("GetDocument() error: %v", err)
	}
	if gotDocument.ID != document.ID || gotDocument.Name != document.Name {
		t.Fatalf("GetDocument() = %#v, want %#v", gotDocument, document)
	}

	documents, err := store.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments() error: %v", err)
	}
	if len(documents) != 1 || documents[0].ID != document.ID {
		t.Fatalf("ListDocuments() = %#v", documents)
	}

	version, err := store.CreateVersion(ctx, document.ID, validStorePolicySource(), "sha-one", true, nil, nil, "initial")
	if err != nil {
		t.Fatalf("CreateVersion() error: %v", err)
	}
	if version.ID == 0 || version.DocumentID != document.ID || version.VersionNumber != 1 || version.Comment == nil || *version.Comment != "initial" {
		t.Fatalf("CreateVersion() = %#v", version)
	}

	sources, err := store.ActiveSources(ctx)
	if err != nil {
		t.Fatalf("ActiveSources() before activate error: %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("ActiveSources() before activate = %#v, want empty", sources)
	}

	generation, err := store.Activate(ctx, document.ID, version.ID)
	if err != nil {
		t.Fatalf("Activate() error: %v", err)
	}
	if generation != 2 {
		t.Fatalf("Activate() generation = %d, want 2", generation)
	}

	gotDocument, err = store.GetDocument(ctx, document.ID)
	if err != nil {
		t.Fatalf("GetDocument() after activate error: %v", err)
	}
	if gotDocument.ActiveVersionID == nil || *gotDocument.ActiveVersionID != version.ID {
		t.Fatalf("active_version_id = %#v, want %d", gotDocument.ActiveVersionID, version.ID)
	}

	sources, err = store.ActiveSources(ctx)
	if err != nil {
		t.Fatalf("ActiveSources() after activate error: %v", err)
	}
	source, ok := sources["scope"]
	if !ok || source.DocumentID != document.ID || source.VersionID != version.ID || source.Source != validStorePolicySource() {
		t.Fatalf("ActiveSources()[scope] = %#v, ok=%t", source, ok)
	}

	currentGeneration, err := store.Generation(ctx)
	if err != nil {
		t.Fatalf("Generation() error: %v", err)
	}
	if currentGeneration != generation {
		t.Fatalf("Generation() = %d, want %d", currentGeneration, generation)
	}
}

func TestPolicyStoreCreateVersionConcurrentMonotonic(t *testing.T) {
	ctx := context.Background()
	_, store := newPolicyStoreTest(t, ctx)

	document, err := store.CreateDocument(ctx, "scope", "household scope")
	if err != nil {
		t.Fatalf("CreateDocument() error: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan Version, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			version, err := store.CreateVersion(ctx, document.ID, validStorePolicySource(), "sha", true, nil, nil, "")
			if err != nil {
				errs <- err
				return
			}
			results <- version
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("CreateVersion() concurrent error: %v", err)
	}

	var numbers []int
	for version := range results {
		numbers = append(numbers, version.VersionNumber)
	}
	sort.Ints(numbers)
	if len(numbers) != 2 || numbers[0] != 1 || numbers[1] != 2 {
		t.Fatalf("version numbers = %#v, want [1 2]", numbers)
	}
}

func TestPolicyStoreActivateConcurrentBumpsGeneration(t *testing.T) {
	ctx := context.Background()
	_, store := newPolicyStoreTest(t, ctx)

	document, err := store.CreateDocument(ctx, "scope", "household scope")
	if err != nil {
		t.Fatalf("CreateDocument() error: %v", err)
	}
	versionOne, err := store.CreateVersion(ctx, document.ID, validStorePolicySource(), "sha-one", true, nil, nil, "")
	if err != nil {
		t.Fatalf("CreateVersion(1) error: %v", err)
	}
	versionTwo, err := store.CreateVersion(ctx, document.ID, validStorePolicySource(), "sha-two", true, nil, nil, "")
	if err != nil {
		t.Fatalf("CreateVersion(2) error: %v", err)
	}

	startGeneration, err := store.Generation(ctx)
	if err != nil {
		t.Fatalf("Generation() before activate error: %v", err)
	}

	start := make(chan struct{})
	generations := make(chan int64, 2)
	errs := make(chan error, 2)
	for _, versionID := range []int64{versionOne.ID, versionTwo.ID} {
		versionID := versionID
		go func() {
			<-start
			generation, err := store.Activate(ctx, document.ID, versionID)
			if err != nil {
				errs <- err
				return
			}
			generations <- generation
		}()
	}
	close(start)

	var returned []int64
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			t.Fatalf("Activate() concurrent error: %v", err)
		case generation := <-generations:
			returned = append(returned, generation)
		}
	}
	sort.Slice(returned, func(i, j int) bool { return returned[i] < returned[j] })
	if returned[0] == returned[1] {
		t.Fatalf("Activate() returned duplicate generations: %#v", returned)
	}

	finalGeneration, err := store.Generation(ctx)
	if err != nil {
		t.Fatalf("Generation() after activate error: %v", err)
	}
	if finalGeneration != startGeneration+2 {
		t.Fatalf("final generation = %d, want %d", finalGeneration, startGeneration+2)
	}
	if returned[0] != startGeneration+1 || returned[1] != startGeneration+2 {
		t.Fatalf("returned generations = %#v, want [%d %d]", returned, startGeneration+1, startGeneration+2)
	}
}

func newPolicyStoreTest(t *testing.T, ctx context.Context) (*pgxpool.Pool, *PolicyStore) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.policy_documents')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check policy_documents table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied policy foundation migration")
	}

	resetPolicyTables(t, ctx, pool)
	return pool, NewPolicyStore(pool)
}

func resetPolicyTables(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE public.policy_decisions RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate policy_decisions: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE public.policy_documents, public.policy_document_versions RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate policy documents: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.policy_generation (id, generation)
		VALUES (true, 1)
		ON CONFLICT (id)
		DO UPDATE SET generation = 1, updated_at = now()`); err != nil {
		t.Fatalf("reset policy generation: %v", err)
	}
}

func validStorePolicySource() string {
	return `package silo_custom.scope

import rego.v1

override(base, _) := base`
}
