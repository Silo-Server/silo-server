package jellycompat

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSmartCollectionStructureAdmission(t *testing.T) {
	for _, rules := range []int{128, 129} {
		t.Run(strconv.Itoa(rules)+"_rules", func(t *testing.T) {
			definition := catalog.QueryDefinition{MediaScope: "movie", Groups: []catalog.QueryGroup{{Rules: make([]catalog.QueryRule, rules)}}}
			for i := range definition.Groups[0].Rules {
				definition.Groups[0].Rules[i] = catalog.QueryRule{Field: "year", Op: "gte", Value: 2000 + i}
			}
			raw, err := json.Marshal(definition)
			if err != nil {
				t.Fatal(err)
			}
			collection := &models.LibraryCollection{ID: "synthetic", LibraryID: 1, CollectionType: "smart", QueryDefinition: raw}
			h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{{ID: 1, Name: "Synthetic", Type: "movies"}}, nil)
			h.queryExecutor = noSmartExecutor{}
			_, _, ok := h.prepareSmartCollectionQuery(t.Context(), nil, collection)
			_, leaf := smartCollectionLeafType(collection)
			if ok != (rules == 128) || leaf != (rules == 128) {
				t.Fatalf("rules=%d: query admitted=%v leaf=%v", rules, ok, leaf)
			}
		})
	}
}

// noSmartExecutor is an executor that must not matter: admission decides
// before any query runs.
type noSmartExecutor struct{}

func (noSmartExecutor) Preview(context.Context, catalog.QueryDefinition, catalog.AccessFilter, int) ([]*models.MediaItem, int, error) {
	return nil, 0, nil
}

func (noSmartExecutor) PreviewPage(context.Context, catalog.QueryDefinition, catalog.AccessFilter, int, int, bool) ([]*models.MediaItem, int, bool, error) {
	return nil, 0, true, nil
}
