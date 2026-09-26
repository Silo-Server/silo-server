package auth

import (
	"errors"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestInviteCodeNamedCreationAndPaging(t *testing.T) {
	// This helper gives each test its own schema; no shared tables are modified.
	pool := apiKeyMetadataRepository(t).pool
	_, err := pool.Exec(t.Context(), `CREATE TABLE invite_codes (id SERIAL PRIMARY KEY,code TEXT UNIQUE NOT NULL,label TEXT NOT NULL,max_uses INTEGER NOT NULL,use_count INTEGER NOT NULL DEFAULT 0,created_by INTEGER NOT NULL,enabled BOOLEAN NOT NULL DEFAULT true,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewInviteCodeRepository(pool)
	input := models.CreateInviteCodeInput{Code: "SYNTHETIC-ONE", Label: "Fixture", MaxUses: 3, CreatedBy: 2}
	var wg sync.WaitGroup
	ids := make(chan int, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			row, err := repo.CreateNamed(t.Context(), input)
			if err != nil {
				errs <- err
				return
			}
			ids <- row.ID
		})
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	id := 0
	for got := range ids {
		if id != 0 && id != got {
			t.Fatal("duplicate creation")
		}
		id = got
	}
	if id == 0 {
		t.Fatal("no code created")
	}
	if err := repo.RedeemCode(t.Context(), input.Code); err != nil {
		t.Fatal(err)
	}
	row, err := repo.CreateNamed(t.Context(), input)
	if err != nil || row.ID != id || row.UseCount != 1 {
		t.Fatal(row, err)
	}
	conflict := input
	conflict.MaxUses++
	if _, err := repo.CreateNamed(t.Context(), conflict); !errors.Is(err, ErrInviteCodeConflict) {
		t.Fatal(err)
	}
	second := input
	second.Code = "SYNTHETIC-TWO"
	newer, err := repo.CreateNamed(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	page, more, err := repo.ListPage(t.Context(), 0, 1)
	if err != nil || !more || len(page) != 1 || page[0].ID != newer.ID {
		t.Fatal(page, more, err)
	}
	page, more, err = repo.ListPage(t.Context(), newer.ID, 1)
	if err != nil || more || len(page) != 1 || page[0].ID != id {
		t.Fatal(page, more, err)
	}
}
