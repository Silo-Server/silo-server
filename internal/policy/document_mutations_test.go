package policy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
)

func policyFixture(t *testing.T, store *PolicyStore) (Document, Version) {
	t.Helper()
	document, err := store.CreateDocument(t.Context(), "scope", "revision test")
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.CreateVersion(t.Context(), document.ID, validStorePolicySource(), "sha", true, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	document, err = store.GetDocument(t.Context(), document.ID)
	if err != nil {
		t.Fatal(err)
	}
	return document, version
}

func TestDocumentRevisionTracksAllLegacyWriters(t *testing.T) {
	ctx := t.Context()
	_, store := newPolicyStoreTest(t, ctx)
	document, err := store.CreateDocument(ctx, "scope", "legacy writers")
	if err != nil {
		t.Fatal(err)
	}
	if document.Revision != 1 {
		t.Fatalf("initial revision=%d", document.Revision)
	}
	version, err := store.CreateVersion(ctx, document.ID, validStorePolicySource(), "sha", true, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.GetDocumentSnapshot(ctx, document.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Document.Revision != 2 || snapshot.ActiveVersion != nil {
		t.Fatalf("append snapshot=%+v", snapshot)
	}
	generation, err := store.Generation(ctx)
	if err != nil || generation != 1 {
		t.Fatalf("append changed runtime generation: %d %v", generation, err)
	}
	generation, err = store.SetEnabled(ctx, document.ID, false)
	if err != nil || generation != 2 {
		t.Fatalf("disable: %d %v", generation, err)
	}
	generation, err = store.Activate(ctx, document.ID, version.ID)
	if err != nil || generation != 3 {
		t.Fatalf("activate: %d %v", generation, err)
	}
	snapshot, err = store.GetDocumentSnapshot(ctx, document.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Document.Revision != 4 || snapshot.ActiveVersion == nil || snapshot.ActiveVersion.ID != *snapshot.Document.ActiveVersionID {
		t.Fatalf("canonical snapshot=%+v", snapshot)
	}
	if err = store.DeleteDocument(ctx, document.ID); !errors.Is(err, ErrDocumentHasActiveVersion) {
		t.Fatalf("active delete=%v", err)
	}
	other, err := store.CreateDocument(ctx, "permission", "delete me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateVersion(ctx, other.ID, "invalid saved draft", "invalid", false, new("compile error"), nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteDocument(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetDocumentSnapshot(ctx, other.ID); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("deleted snapshot=%v", err)
	}
	generation, err = store.Generation(ctx)
	if err != nil || generation != 3 {
		t.Fatalf("create/delete changed runtime generation: %d %v", generation, err)
	}
}

func TestDocumentCASOriginalWitnessNoOpAndWildcard(t *testing.T) {
	ctx := t.Context()
	_, store := newPolicyStoreTest(t, ctx)
	document, version := policyFixture(t, store)
	if _, err := store.SetEnabled(ctx, document.ID, false); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetDocument(ctx, document.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func() error{
		"stale no-op": func() error {
			_, err := store.SetEnabledIfRevision(ctx, document.ID, false, document.Revision)
			return err
		},
		"stale activation": func() error {
			_, err := store.ActivateIfRevision(ctx, document.ID, version.ID, document.Revision)
			return err
		},
		"stale delete": func() error { return store.DeleteDocumentIfRevision(ctx, document.ID, document.Revision) },
	} {
		t.Run(name, func(t *testing.T) {
			err := mutate()
			stale, ok := errors.AsType[*DocumentRevisionMismatchError](err)
			if !ok || stale.Expected != document.Revision || stale.Actual != before.Revision {
				t.Fatalf("stale original witness: %v", err)
			}
		})
	}
	after, err := store.GetDocument(ctx, document.ID)
	if err != nil || after.Revision != before.Revision {
		t.Fatalf("stale write changed revision: %+v %v", after, err)
	}
	generation, err := store.Generation(ctx)
	if err != nil || generation != 2 {
		t.Fatalf("stale write changed generation: %d %v", generation, err)
	}
	noop, err := store.SetEnabledIfRevision(ctx, document.ID, false, before.Revision)
	if err != nil || noop.Generation != 3 || noop.Document.Revision != before.Revision+1 {
		t.Fatalf("exact no-op preserves legacy generation semantics: %+v %v", noop, err)
	}
	applied, err := store.ActivateIfRevision(ctx, document.ID, version.ID, AnyDocumentRevision)
	if err != nil || applied.Generation != 4 || applied.Document.Enabled {
		t.Fatalf("wildcard activation: %+v %v", applied, err)
	}
	if err = store.DeleteDocumentIfRevision(ctx, document.ID, AnyDocumentRevision); !errors.Is(err, ErrDocumentHasActiveVersion) {
		t.Fatalf("wildcard bypassed constraint: %v", err)
	}
	if err = store.DeleteDocumentIfRevision(ctx, 999999, AnyDocumentRevision); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("wildcard missing=%v", err)
	}
}

// The legacy writer pauses inside a real PostgreSQL trigger after acquiring
// its document row. The contender must actually wait for that row before the
// test releases the writer; elapsed sleeps never establish the ordering.
func pausePolicyWriter(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	ctx := t.Context()
	_, err := pool.Exec(ctx, `CREATE FUNCTION policy_test_pause_writer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(829134); RETURN NEW; END $$; CREATE TRIGGER policy_test_pause_writer AFTER UPDATE ON policy_documents FOR EACH ROW EXECUTE FUNCTION policy_test_pause_writer()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS policy_test_pause_writer ON policy_documents; DROP FUNCTION IF EXISTS policy_test_pause_writer()`)
	})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(829134)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}
func namedPolicyStore(t *testing.T, pool *pgxpool.Pool, name string) *PolicyStore {
	t.Helper()
	cfg := pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["application_name"] = name
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return NewPolicyStore(p)
}
func waitPolicyLock(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, name).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("writer never reached PostgreSQL lock barrier")
		case <-ticker.C:
		}
	}
}
func TestPolicyLegacyWriterVersusCAS(t *testing.T) {
	for _, writer := range []string{"append", "enable", "activate"} {
		for _, operation := range []string{"enable", "activate", "delete"} {
			for _, wildcard := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/wildcard=%t", writer, operation, wildcard), func(t *testing.T) {
					ctx := t.Context()
					pool, store := newPolicyStoreTest(t, ctx)
					document, version := policyFixture(t, store)
					writerStore := namedPolicyStore(t, pool, "policy-revision-writer")
					contender := namedPolicyStore(t, pool, "policy-revision-contender")
					barrier := pausePolicyWriter(t, pool)
					written := make(chan error, 1)
					go func() {
						var err error
						switch writer {
						case "append":
							_, err = writerStore.CreateVersion(ctx, document.ID, validStorePolicySource(), "new", true, nil, nil, "")
						case "enable":
							_, err = writerStore.SetEnabled(ctx, document.ID, false)
						case "activate":
							_, err = writerStore.Activate(ctx, document.ID, version.ID)
						}
						written <- err
					}()
					waitPolicyLock(t, pool, "policy-revision-writer")
					expected := document.Revision
					if wildcard {
						expected = AnyDocumentRevision
					}
					result := make(chan error, 1)
					go func() {
						var err error
						switch operation {
						case "enable":
							_, err = contender.SetEnabledIfRevision(ctx, document.ID, false, expected)
						case "activate":
							_, err = contender.ActivateIfRevision(ctx, document.ID, version.ID, expected)
						case "delete":
							err = contender.DeleteDocumentIfRevision(ctx, document.ID, expected)
						}
						result <- err
					}()
					waitPolicyLock(t, pool, "policy-revision-contender")
					if err := barrier.Commit(ctx); err != nil {
						t.Fatal(err)
					}
					if err := <-written; err != nil {
						t.Fatal(err)
					}
					err := <-result
					if wildcard && operation == "delete" && writer == "activate" {
						if !errors.Is(err, ErrDocumentHasActiveVersion) {
							t.Fatalf("active-delete constraint=%v", err)
						}
						return
					}
					if wildcard && err != nil {
						t.Fatal(err)
					}
					if wildcard && operation == "delete" {
						if _, err := store.GetDocument(ctx, document.ID); !errors.Is(err, ErrDocumentNotFound) {
							t.Fatalf("wildcard deletion=%v", err)
						}
						return
					}
					if !wildcard && !errors.Is(err, ErrDocumentRevisionMismatch) {
						t.Fatalf("expected stale original witness, got %v", err)
					}
					got, err := store.GetDocument(ctx, document.ID)
					if err != nil {
						t.Fatal(err)
					}
					want := document.Revision + 1
					if wildcard {
						want++
					}
					if got.Revision != want {
						t.Fatalf("revision=%d want%d", got.Revision, want)
					}
				})
			}
		}
	}
}

type failingPolicyPublish struct{ cache.NoopEventBus }

func (*failingPolicyPublish) Publish(context.Context, string, cache.Event) error {
	return errors.New("test publish failure")
}
func TestDocumentServiceCommittedApplyFailure(t *testing.T) {
	for _, failure := range []string{"local reload", "event publish"} {
		t.Run(failure, func(t *testing.T) {
			ctx := t.Context()
			pool, store := newPolicyStoreTest(t, ctx)
			document, version := policyFixture(t, store)
			runtimeStore := namedPolicyStore(t, pool, "policy-runtime")
			system := newStartedPolicySystem(t, ctx, runtimeStore, &failingPolicyPublish{}, time.Hour)
			if failure == "local reload" {
				runtimeStore.pool.Close()
			}
			service := NewDocumentService(store, system)
			result, err := service.Activate(ctx, document.ID, version.ID, document.Revision)
			if err != nil || !result.Persisted || result.Generation != 2 || result.Document.ActiveVersionID == nil {
				t.Fatalf("committed result lost: %+v %v", result, err)
			}
			if result.Application.PublishErr == nil {
				t.Fatal("publication failure omitted")
			}
			if failure == "local reload" && result.Application.LocalReloadErr == nil {
				t.Fatal("reload failure omitted")
			}
			if failure == "event publish" && (!result.Application.Applied() || result.Application.Generation != 2) {
				t.Fatalf("local application lost: %+v", result.Application)
			}
			got, err := store.GetDocumentSnapshot(ctx, document.ID)
			if err != nil || got.ActiveVersion == nil || got.ActiveVersion.ID != version.ID {
				t.Fatalf("commit missing: %+v %v", got, err)
			}
			if _, err = service.Activate(ctx, document.ID, version.ID, document.Revision); !errors.Is(err, ErrDocumentRevisionMismatch) {
				t.Fatalf("old witness replay accepted: %v", err)
			}
			generation, err := store.Generation(ctx)
			if err != nil || generation != 2 {
				t.Fatalf("replay changed generation: %d %v", generation, err)
			}
		})
	}
}

func TestPolicyGuardedConstraintsRollbackRevisionAndGeneration(t *testing.T) {
	ctx := t.Context()
	_, store := newPolicyStoreTest(t, ctx)
	document, _ := policyFixture(t, store)
	if _, err := store.SetEnabled(ctx, document.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDocument(ctx, "scope", "other enabled document"); err != nil {
		t.Fatal(err)
	}
	bad, err := store.CreateVersion(ctx, document.ID, "bad source", "bad", false, new("compile failure"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	slow := `package silo_custom.scope
import rego.v1
override(base, _) := base if {
 count([x | some i in numbers.range(1,10000); some j in numbers.range(1,10000); x := i+j]) > 0
}`
	slowVersion, err := store.CreateVersion(ctx, document.ID, slow, "slow", true, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetDocument(ctx, document.ID)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := store.Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		want   error
		mutate func() error
	}{
		{"one enabled per domain", ErrDomainAlreadyEnabled, func() error {
			_, err := store.SetEnabledIfRevision(ctx, document.ID, true, before.Revision)
			return err
		}},
		{"failed compilation", ErrVersionNotCompiled, func() error {
			_, err := store.ActivateIfRevision(ctx, document.ID, bad.ID, before.Revision)
			return err
		}},
		{"evaluation budget", ErrPolicySlowEval, func() error {
			_, err := store.ActivateIfRevision(ctx, document.ID, slowVersion.ID, before.Revision, time.Millisecond)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.mutate(); !errors.Is(err, tc.want) {
				t.Fatalf("got%v want%v", err, tc.want)
			}
			after, err := store.GetDocument(ctx, document.ID)
			if err != nil || after.Revision != before.Revision || after.Enabled || after.ActiveVersionID != nil {
				t.Fatalf("failed mutation changed document: %+v %v", after, err)
			}
			got, err := store.Generation(ctx)
			if err != nil || got != generation {
				t.Fatalf("failed mutation changed generation: %d %v", got, err)
			}
		})
	}
}
