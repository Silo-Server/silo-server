package userdb

import (
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestSQLiteCollectionCompositionRecovery(t *testing.T) {
	s := newConformanceStore(t).(*SQLiteUserStore)
	ctx := t.Context()
	legacy := `{"groups":[` + strings.Repeat("{},", 16) + `{}]}`
	c, err := s.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Legacy", CollectionType: "smart", QueryDefinition: legacy})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", Name: new("Synthetic renamed")}); err != nil {
		t.Fatalf("legacy metadata edit: %v", err)
	}
	revision, err := s.CollectionRevision(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", QueryDefinition: new(`{}`), ExpectedRevision: &revision, ValidateQueryComposition: catalog.ValidatePersonalCollectionQueryBudget}
	if err := s.UpdateCollection(ctx, input); err != nil {
		t.Fatalf("revision-guarded repair: %v", err)
	}
	if err := s.UpdateCollection(ctx, input); !errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		t.Fatalf("stale v2 revision: %v", err)
	}
	for _, guarded := range []bool{false, true} {
		revision, err = s.CollectionRevision(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		input.QueryDefinition, input.ExpectedRevision = &legacy, nil
		if guarded {
			input.ExpectedRevision = &revision
		}
		if err := s.UpdateCollection(ctx, input); !errors.Is(err, catalog.ErrQueryComplexity) {
			t.Fatalf("invalid definition accepted: %v", err)
		}
		after, err := s.CollectionRevision(ctx, c.ID)
		if err != nil || after != revision {
			t.Fatalf("rejected mutation consumed revision: %d -> %d, %v", revision, after, err)
		}
	}
	stored, err := s.GetCollection(ctx, c.ID)
	if err != nil || stored.QueryDefinition != "{}" || stored.Name != "Synthetic renamed" {
		t.Fatalf("rejected definition persisted or metadata lost: %+v, %v", stored, err)
	}
	if err := s.DeleteCollection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
}
