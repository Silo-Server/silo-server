package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestDeletePolicyAuthorAndInactiveDocumentLockOrder(t *testing.T) {
	ctx := t.Context()
	pool, store := newPolicyStoreTest(t, ctx)
	var author int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,role,enabled) VALUES('policy-lock-author','policy-lock-author@example.invalid','x','user',true) RETURNING id`).Scan(&author); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, author) })
	doc, err := store.CreateDocument(ctx, "scope", "attributed inactive draft")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateVersion(ctx, doc.ID, validStorePolicySource(), "sha", true, nil, &author, ""); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// This is the exact lock and delete order used by DeleteDocument.
	if _, err = lockPolicyDocument(ctx, tx, doc.ID, nil); err != nil {
		t.Fatal(err)
	}
	accountPool := namedPolicyStore(t, pool, "policy-delete-author").pool
	deleted := make(chan error, 1)
	go func() { deleted <- auth.NewUserRepository(accountPool).Delete(ctx, author) }()
	waitPolicyLock(t, pool, "policy-delete-author")
	_, deleteErr := tx.Exec(ctx, `DELETE FROM policy_documents WHERE id=$1`, doc.ID)
	if deleteErr != nil {
		_ = tx.Rollback(ctx)
	} else {
		deleteErr = tx.Commit(ctx)
	}
	accountErr := <-deleted
	if deleteErr != nil || accountErr != nil {
		t.Fatalf("concurrent deletes: document=%v account=%v", deleteErr, accountErr)
	}
}

func TestDeletePolicyAuthorVersusVersionAppendLocksAuthorFirst(t *testing.T) {
	ctx := t.Context()
	pool, store := newPolicyStoreTest(t, ctx)
	var author int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,role,enabled) VALUES('policy-append-author','policy-append-author@example.invalid','x','user',true) RETURNING id`).Scan(&author); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, author) })
	doc, err := store.CreateDocument(ctx, "scope", "attributed draft")
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.CreateVersion(ctx, doc.ID, validStorePolicySource(), "sha", true, nil, &author, "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = lockPolicyDocument(ctx, tx, doc.ID, nil); err != nil {
		t.Fatal(err)
	}
	accountPool := namedPolicyStore(t, pool, "policy-delete-author").pool
	deleted := make(chan error, 1)
	go func() { deleted <- auth.NewUserRepository(accountPool).Delete(ctx, author) }()
	waitPolicyLock(t, pool, "policy-delete-author")
	appendStore := namedPolicyStore(t, pool, "policy-append-author")
	appended := make(chan error, 1)
	go func() {
		_, err := appendStore.CreateVersion(ctx, doc.ID, validStorePolicySource(), "next", true, nil, &author, "")
		appended <- err
	}()
	waitPolicyLock(t, pool, "policy-append-author")
	var waitsOnAuthor bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_stat_activity waiter
 JOIN pg_stat_activity blocker ON blocker.pid=ANY(pg_blocking_pids(waiter.pid))
 WHERE waiter.application_name='policy-append-author' AND blocker.application_name='policy-delete-author'
 )`).Scan(&waitsOnAuthor); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	deleteErr, appendErr := <-deleted, <-appended
	if !waitsOnAuthor {
		t.Fatal("append locked the document before acquiring its author")
	}
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	foreignKey, ok := errors.AsType[*pgconn.PgError](appendErr)
	if !ok || foreignKey.Code != "23503" {
		t.Fatalf("append after author deletion=%v", appendErr)
	}
	after, err := store.GetDocument(ctx, doc.ID)
	if err != nil || after.Revision != before.Revision+1 {
		t.Fatalf("author metadata revision=%+v %v", after, err)
	}
	updated, err := store.GetVersion(ctx, doc.ID, version.ID)
	if err != nil || updated.CreatedByUserID != nil {
		t.Fatalf("author metadata=%+v %v", updated, err)
	}
	generation, err := store.Generation(ctx)
	if err != nil || generation != 1 {
		t.Fatalf("metadata changed runtime generation=%d %v", generation, err)
	}
}
