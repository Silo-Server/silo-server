package auth

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Exercise the real repository against a task-owned schema. Do not print the
// generated bearer, including on failure.
func TestAPIKeyStorageDoesNotRetainBearer(t *testing.T) {
	repo := apiKeyMetadataRepository(t)
	ctx := t.Context()
	created, err := repo.Create(ctx, 2, "Synthetic storage regression", []string{ScopeAdminUsers})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Key, "sa_") || len(created.Key) != 67 {
		t.Fatal("creation did not disclose the expected bearer format")
	}
	var storedBearer bool
	if err := repo.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys ak, jsonb_each_text(to_jsonb(ak)) field WHERE ak.id=$1 AND field.value=$2)`, created.ID, created.Key).Scan(&storedBearer); err != nil {
		t.Fatal(err)
	}
	if storedBearer {
		t.Fatal("database row retains the reusable bearer")
	}
	for _, read := range []func() (any, error){
		func() (any, error) { return repo.GetByID(ctx, created.ID) },
		func() (any, error) { return repo.GetByKey(ctx, created.Key) },
		func() (any, error) { return repo.GetMetadataByID(ctx, created.ID) },
		func() (any, error) { return repo.ListByUser(ctx, 2) },
		func() (any, error) { return repo.ListAll(ctx) },
	} {
		result, err := read()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), created.Key) {
			t.Fatal("repository read hydrated the reusable bearer")
		}
	}
	altered := created.Key[:len(created.Key)-1] + "x"
	if _, err := repo.GetByKey(ctx, altered); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatal("altered bearer was not rejected")
	}
	if err := repo.Delete(ctx, created.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetByKey(ctx, created.Key); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatal("revoked bearer was not rejected")
	}
	t.Log("storage, repository reads, valid/altered/revoked controls passed; no bearer values recorded")
}
