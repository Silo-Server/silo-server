package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func budgetRules(n int) []QueryRule {
	rules := make([]QueryRule, n)
	for i := range rules {
		rules[i] = QueryRule{Field: "year", Op: "gte", Value: 2000}
	}
	return rules
}

func budgetDefinitionJSON(t *testing.T, def QueryDefinition) string {
	t.Helper()
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestQueryStructureRawBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		def  QueryDefinition
	}{
		{"groups", QueryDefinition{Groups: make([]QueryGroup, queryMaxGroups)}},
		{"rules", QueryDefinition{Groups: []QueryGroup{{Rules: budgetRules(queryMaxRules)}}}},
		{"libraries before deduplication", QueryDefinition{LibraryIDs: make([]int, queryMaxLibraries)}},
		{"aggregate encoded values", QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Value: strings.Repeat("a", queryMaxValueBytes/2-2)}, {Value: strings.Repeat("b", queryMaxValueBytes/2-2)}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateQueryStructure(tc.def); err != nil {
				t.Fatalf("exact boundary: %v", err)
			}
			if _, err := validateQueryJSONStructure([]byte(budgetDefinitionJSON(t, tc.def))); err != nil {
				t.Fatalf("raw exact boundary: %v", err)
			}
			switch tc.name {
			case "groups":
				tc.def.Groups = append(tc.def.Groups, QueryGroup{})
			case "rules":
				tc.def.Groups[0].Rules = append(tc.def.Groups[0].Rules, QueryRule{})
			case "libraries before deduplication":
				for i := range tc.def.LibraryIDs {
					tc.def.LibraryIDs[i] = 1
				}
				tc.def.LibraryIDs = append(tc.def.LibraryIDs, 1)
			default:
				tc.def.Groups[0].Rules[1].Value = strings.Repeat("b", queryMaxValueBytes/2-1)
			}
			if err := ValidateQueryStructure(tc.def); !errors.Is(err, ErrQueryComplexity) {
				t.Fatalf("over boundary: %v", err)
			}
			if err := tc.def.Normalize().Validate(); !errors.Is(err, ErrQueryComplexity) {
				t.Fatalf("normalization erased raw failure: %v", err)
			}
			if _, err := validateQueryJSONStructure([]byte(budgetDefinitionJSON(t, tc.def))); !errors.Is(err, ErrQueryComplexity) {
				t.Fatalf("raw over boundary: %v", err)
			}
		})
	}
}

func TestQueryStructureCompositionAndCleanup(t *testing.T) {
	source := QueryDefinition{Groups: []QueryGroup{{Rules: budgetRules(127)}}}
	display := `{"groups":[{"rules":[{"field":"type","op":"is","value":"all"}]}]}`
	if err := ValidatePersonalCollectionQueryBudget(budgetDefinitionJSON(t, source), display); err != nil {
		t.Fatalf("128 composed rules: %v", err)
	}
	if err := validateCollectionQueryComposition(budgetDefinitionJSON(t, source), display, QueryDefinition{Groups: []QueryGroup{{Rules: budgetRules(1)}}}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("source/display/overlay: %v", err)
	}
	source.Groups[0].Rules = append(source.Groups[0].Rules, budgetRules(1)...)
	if _, _, err := personalCollectionCountQuery(1, PersonalCollectionDefinition{CollectionType: "smart", QueryDefinition: budgetDefinitionJSON(t, source), DisplayQueryDefinition: display}, AccessFilter{}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("display cleanup hid composed raw rule: %v", err)
	}
	display = `{"groups":[` + strings.Repeat(`{"rules":[]},`, 16) + `{"rules":[]}]}`
	if _, err := NormalizeDisplayQueryFragment([]byte(display)); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("empty group cleanup hid raw groups: %v", err)
	}
	if err := ValidateQueryStructure(QueryDefinition{Groups: make([]QueryGroup, 8)}, QueryDefinition{Groups: make([]QueryGroup, 9)}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("composed groups: %v", err)
	}
}

func TestQueryStructureComposedLibrariesAndValueBytes(t *testing.T) {
	source := QueryDefinition{LibraryIDs: make([]int, queryMaxLibraries/2)}
	overlay := QueryDefinition{LibraryIDs: make([]int, queryMaxLibraries/2)}
	if err := validateCollectionQueryComposition(budgetDefinitionJSON(t, source), "", overlay); err != nil {
		t.Fatalf("composed library boundary: %v", err)
	}
	overlay.LibraryIDs = append(overlay.LibraryIDs, 1)
	if err := validateCollectionQueryComposition(budgetDefinitionJSON(t, source), "", overlay); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("composed library overflow: %v", err)
	}
	source = QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "title", Op: "contains", Value: strings.Repeat("a", queryMaxValueBytes/2-2)}}}}}
	overlay = QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "title", Op: "contains", Value: strings.Repeat("b", queryMaxValueBytes/2-2)}}}}}
	if err := validateCollectionQueryComposition(budgetDefinitionJSON(t, source), "", overlay); err != nil {
		t.Fatalf("composed value boundary: %v", err)
	}
	overlay.Groups[0].Rules[0].Value = strings.Repeat("b", queryMaxValueBytes/2-1)
	if err := validateCollectionQueryComposition(budgetDefinitionJSON(t, source), "", overlay); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("composed value overflow: %v", err)
	}
}

func TestQueryStructureEncodedAndLegacyControls(t *testing.T) {
	for _, key := range []string{`library_ids`, `\u006cibrary_ids`, `filter_library_ids`} {
		raw := `{"` + key + `":[` + strings.Repeat("1,", 256) + `1]}`
		if _, err := parseCatalogCollectionQueryDefinition([]byte(raw)); !errors.Is(err, ErrQueryComplexity) {
			t.Errorf("%s duplicates: %v", key, err)
		}
	}
	// A repeated key cannot discard the work admitted before the last value.
	raw := `{"groups":[` + strings.Repeat(`{},`, 15) + `{}],"groups":[{}]}`
	if _, err := validateQueryJSONStructure([]byte(raw)); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("repeated groups: %v", err)
	}
	oversizedEncoded := `{"groups":[{"rules":[{"value":"` + strings.Repeat(`\u0061`, queryMaxValueBytes/6+1) + `"}]}]}`
	if _, err := validateQueryJSONStructure([]byte(oversizedEncoded)); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("encoded rather than decoded value bytes: %v", err)
	}
	if err := (QueryDefinition{LibraryIDs: []int{-1}}).Validate(); err == nil || errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("negative ID should retain semantic validation: %v", err)
	}
	if err := ValidatePersonalCollectionQueryBudget(`{`, ""); err == nil || errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("malformed JSON classification: %v", err)
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if err := ValidateQueryStructure(QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Value: cyclic}}}}}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("cyclic value: %v", err)
	}
}

func TestQueryStructureSharedBuilderAndExecutor(t *testing.T) {
	def := QueryDefinition{LibraryIDs: make([]int, queryMaxLibraries+1)}
	for i := range def.LibraryIDs {
		def.LibraryIDs[i] = 1
	}
	if _, _, err := NewQueryBuilder("mi").Build(def); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("builder raw admission: %v", err)
	}
	if _, _, err := (&QueryExecutor{}).buildCountQuery(def, AccessFilter{}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("count raw admission: %v", err)
	}
	if _, err := (&QueryExecutor{}).buildPreviewPagePlan(def, AccessFilter{}, 1, 0); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("preview raw admission: %v", err)
	}
	// Structural limits do not limit membership or remove source ORDER/LIMIT.
	valid := ApplySmartCollectionItemLimit(QueryDefinition{Sort: QuerySort{Field: "year", Order: "desc"}})
	predicate, args, err := collectionDefinitionPredicate(&QueryExecutor{}, valid, AccessFilter{})
	if err != nil || !strings.Contains(predicate, "ORDER BY") || !strings.Contains(predicate, "LIMIT") || args[len(args)-1] != DefaultSmartCollectionItemLimit {
		t.Fatalf("source semantics changed: %q %v %v", predicate, args, err)
	}
}

func TestQueryStructureURLAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values url.Values
	}{
		{"ratings before empty cleanup", url.Values{"content_rating": {strings.Repeat(",", queryMaxRules)}}},
		{"value before whitespace cleanup", url.Values{"groups[0][rules][0][value]": {strings.Repeat(" ", queryMaxValueBytes+1)}}},
		{"duplicate library IDs", url.Values{"library_id": make([]string, queryMaxLibraries+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCatalogRequestWithOptions(tc.values, CatalogRequestOptions{ExtendedRules: true}); !errors.Is(err, ErrQueryComplexity) {
				t.Fatalf("URL raw admission: %v", err)
			}
		})
	}
	values := url.Values{}
	for i := range queryMaxRules {
		values.Set(fmt.Sprintf("groups[0][rules][%d][field]", i), "year")
		values.Set(fmt.Sprintf("groups[0][rules][%d][op]", i), "gte")
		values.Set(fmt.Sprintf("groups[0][rules][%d][value]", i), "2000")
	}
	if _, err := ParseCatalogRequestWithOptions(values, CatalogRequestOptions{ExtendedRules: true}); err != nil {
		t.Fatalf("URL exact rule boundary: %v", err)
	}
	values.Set("genre", "Synthetic")
	if _, err := ParseCatalogRequestWithOptions(values, CatalogRequestOptions{ExtendedRules: true}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("implicit/explicit composition: %v", err)
	}
}

func TestQueryStructureRawEntryPoints(t *testing.T) {
	raw := budgetDefinitionJSON(t, QueryDefinition{Groups: make([]QueryGroup, queryMaxGroups+1)})
	if _, err := (&PersonalCollectionCollages{}).smartSources(t.Context(), raw, AccessFilter{}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("collage admission: %v", err)
	}
	if _, _, err := QueryCollectionItemsBySort(t.Context(), nil, nil, QuerySort{}, AccessFilter{}, 1, raw); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("sort admission: %v", err)
	}
	repo := &LibraryCollectionRepository{}
	if _, err := repo.Create(t.Context(), CreateLibraryCollectionInput{QueryDefinition: []byte(raw)}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("library save admission: %v", err)
	}
	if err := repo.Update(t.Context(), UpdateLibraryCollectionInput{QueryDefinition: []byte(raw)}); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("library update admission: %v", err)
	}
	// encoding/json matches tagged property names without regard to case.
	source := strings.ReplaceAll(budgetDefinitionJSON(t, QueryDefinition{Groups: []QueryGroup{{Rules: budgetRules(128)}}}), "groups", "GROUPS")
	display := `{"GROUPS":[{"RULES":[{"FIELD":"type","OP":"is","VALUE":"all"}]}]}`
	if err := ValidatePersonalCollectionQueryBudget(source, display); !errors.Is(err, ErrQueryComplexity) {
		t.Fatalf("case-insensitive composition bypass: %v", err)
	}
}
