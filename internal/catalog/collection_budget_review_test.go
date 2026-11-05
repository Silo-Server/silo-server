package catalog

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestQueryBudgetDecoderEquivalentKeys(t *testing.T) {
	source := budgetDefinitionJSON(t, QueryDefinition{Groups: []QueryGroup{{Rules: budgetRules(64)}}})
	// The JSON decoder recognizes the long-s alias as the same field name.
	aliased := strings.Replace(source, `"groups"`, `"groupſ"`, 1)
	if err := ValidatePersonalCollectionQueryBudget(source, aliased); err != nil {
		t.Fatalf("exact combined rule boundary: %v", err)
	}
	display := budgetDefinitionJSON(t, QueryDefinition{Groups: []QueryGroup{{Rules: budgetRules(65)}}})
	display = strings.Replace(display, `"groups"`, `"groupſ"`, 1)
	if err := ValidatePersonalCollectionQueryBudget(source, display); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("decoder-recognized composed fields bypassed admission: %v", err)
	}
}

func TestCollectionCountRejectedDefinitionsShareLedger(t *testing.T) {
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	for owner := range 2 {
		definitions := make([]PersonalCollectionDefinition, 65)
		for i := range definitions {
			definitions[i] = PersonalCollectionDefinition{ID: fmt.Sprintf("rejected-%d-%d", owner, i), CollectionType: "smart", QueryDefinition: fmt.Sprintf(`{"groups":[],"sort":{"field":"invalid-%d-%d"}}`, owner, i)}
		}
		_, err := CountPersonalCollections(ctx, nil, owner+1, definitions, AccessFilter{})
		if err == nil {
			t.Fatal("invalid definitions unexpectedly counted")
		}
		if owner == 1 && !errors.Is(err, ErrCollectionCountBudget) {
			t.Fatalf("later owner's rejected definitions escaped the aggregate budget: %v", err)
		}
	}
	budget := ctx.Value(collectionCountBudgetKey{}).(*collectionCountBudget)
	if len(budget.entries) != collectionCountMaxDefinitions {
		t.Fatalf("ledger retained %d attempts, want %d", len(budget.entries), collectionCountMaxDefinitions)
	}
}

func TestLibraryCollectionCompositionAdmission(t *testing.T) {
	ids := make([]int, queryMaxLibraries)
	for i := range ids {
		ids[i] = i + 1
	}
	collection := &models.LibraryCollection{LibraryIDs: []int{1}, CollectionType: "smart", QueryDefinition: []byte(budgetDefinitionJSON(t, QueryDefinition{LibraryIDs: ids}))}
	if _, err := ResolveLibraryCollectionMembership(collection, nil); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("collection binding and query library composition exceeds 256: %v", err)
	}
	collection.QueryDefinition = []byte(budgetDefinitionJSON(t, QueryDefinition{LibraryIDs: ids[:queryMaxLibraries-1]}))
	if _, err := ResolveLibraryCollectionMembership(collection, nil); err != nil {
		t.Fatalf("exact composed library boundary: %v", err)
	}
}

func TestLibraryCollectionCreateCompositionBeforeDatabase(t *testing.T) {
	ids := make([]int, queryMaxLibraries)
	for i := range ids {
		ids[i] = 1
	}
	repo := NewLibraryCollectionRepository(nil)
	_, err := repo.Create(t.Context(), CreateLibraryCollectionInput{LibraryID: 1, QueryDefinition: []byte(budgetDefinitionJSON(t, QueryDefinition{LibraryIDs: ids}))})
	if !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("raw composed create reached database: %v", err)
	}
}
