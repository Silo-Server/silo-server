package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Both handlers finish reading and admitting the old companion before either
// store mutation starts. The second edit must recheck the committed companion,
// in either write order, rather than its earlier snapshot.
type compositionGateStore struct {
	*pgstore.PostgresUserStore
	prepared       chan struct{}
	sourceRelease  chan struct{}
	displayRelease chan struct{}
}

func (s *compositionGateStore) UpdateCollection(ctx context.Context, input userstore.UpdateCollectionInput) error {
	s.prepared <- struct{}{}
	release := s.displayRelease
	if input.QueryDefinition != nil {
		release = s.sourceRelease
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-release:
		return s.PostgresUserStore.UpdateCollection(ctx, input)
	}
}

func compositionRules(t *testing.T, n int) json.RawMessage {
	t.Helper()
	rules := make([]catalog.QueryRule, n)
	for i := range rules {
		rules[i] = catalog.QueryRule{Field: "type", Op: "is", Value: "movie"}
	}
	raw, err := json.Marshal(map[string]any{"groups": []catalog.QueryGroup{{Rules: rules}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPersonalCollectionCompositionConcurrentUpdatesDB(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rules        int
		displayFirst bool
	}{
		{name: "over-budget-source-first", rules: 65},
		{name: "over-budget-display-first", rules: 65, displayFirst: true},
		{name: "exact-boundary-source-first", rules: 64},
		{name: "exact-boundary-display-first", rules: 64, displayFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testPersonalCollectionCompositionRace(t, tc.rules, tc.displayFirst)
		})
	}
}

func testPersonalCollectionCompositionRace(t *testing.T, displayRules int, displayFirst bool) {
	t.Helper()
	f := newPagingIntegrationFixture(t)
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(t.Context(), f.account)
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Synthetic composition", CollectionType: "smart"})
	if err != nil {
		t.Fatal(err)
	}
	gate := &compositionGateStore{PostgresUserStore: store.(*pgstore.PostgresUserStore), prepared: make(chan struct{}, 2), sourceRelease: make(chan struct{}), displayRelease: make(chan struct{})}
	h := NewCollectionHandler(pagingIntegrationProvider{account: f.account, store: gate})
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sourceDone, displayDone := make(chan error, 1), make(chan error, 1)
	update := func(req PersonalCollectionUpdateRequest, done chan<- error) {
		_, err := h.UpdatePersonalCollection(ctx, PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: "owner", CollectionID: c.ID, Request: req, V1Rules: true})
		done <- err
	}
	go update(PersonalCollectionUpdateRequest{QueryDefinition: compositionRules(t, 64)}, sourceDone)
	go update(PersonalCollectionUpdateRequest{DisplayQueryDefinition: compositionRules(t, displayRules)}, displayDone)
	for range 2 {
		select {
		case <-gate.prepared:
		case err := <-sourceDone:
			t.Fatalf("source patch failed before store admission: %v", err)
		case err := <-displayDone:
			t.Fatalf("display patch failed before store admission: %v", err)
		case <-ctx.Done():
			t.Fatalf("both valid patches did not reach the store: %v", ctx.Err())
		}
	}
	firstRelease, secondRelease := gate.sourceRelease, gate.displayRelease
	firstDone, secondDone := sourceDone, displayDone
	if displayFirst {
		firstRelease, secondRelease = secondRelease, firstRelease
		firstDone, secondDone = secondDone, firstDone
	}
	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first valid patch: %v", err)
	}
	close(secondRelease)
	err = <-secondDone
	if displayRules == 65 {
		assertCompositionFieldError(t, err)
	} else if err != nil {
		t.Fatalf("exact-boundary second patch: %v", err)
	}
	stored, err := store.GetCollection(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidatePersonalCollectionQueryBudget(stored.QueryDefinition, stored.DisplayQueryDefinition); err != nil {
		t.Errorf("persisted combined source/display exceeds budget: %v", err)
	}
	if displayRules == 65 && !displayFirst && stored.DisplayQueryDefinition != "" {
		t.Error("rejected display patch was persisted")
	}
	if displayRules == 65 && displayFirst && stored.QueryDefinition != "{}" {
		t.Error("rejected source patch was persisted")
	}
	if displayRules == 64 && (stored.DisplayQueryDefinition == "" || stored.QueryDefinition == "{}") {
		t.Error("an admitted independent patch was lost")
	}
}

func assertCompositionFieldError(t *testing.T, err error) {
	t.Helper()
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusBadRequest || apiErr.Field != "query_definition" {
		t.Errorf("composition admission = %v, want query_definition validation error", err)
	}
}

type compositionRecheckStore struct {
	lifecycleStore
	lockedSource, lockedDisplay string
	storeError                  error
}

func (s *compositionRecheckStore) UpdateCollection(_ context.Context, input userstore.UpdateCollectionInput) error {
	s.update = input
	if input.ValidateQueryComposition != nil {
		return input.ValidateQueryComposition(s.lockedSource, s.lockedDisplay)
	}
	return s.storeError
}

// These controls isolate retention of request structure erased by normalization.
// The PostgreSQL write-lock test separately verifies read/commit serialization.
func TestPersonalCollectionCompositionRawRecheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request PersonalCollectionUpdateRequest
		source  string
		display string
	}{
		{name: "discarded-display-rules", request: PersonalCollectionUpdateRequest{DisplayQueryDefinition: json.RawMessage(strings.ReplaceAll(string(compositionRules(t, 65)), `"movie"`, `"all"`))}, source: string(compositionRules(t, 64))},
		{name: "discarded-source-groups", request: PersonalCollectionUpdateRequest{QueryDefinition: json.RawMessage(`{"groups":[{},{},{},{},{},{},{},{}]}`)}, display: `{"groups":[{},{},{},{},{},{},{},{},{}]}`},
		{name: "duplicate-source-libraries", request: PersonalCollectionUpdateRequest{QueryDefinition: json.RawMessage(`{"library_ids":[` + strings.Repeat("1,", 255) + `1]}`)}, display: `{"library_ids":[1]}`},
		{name: "combined-value-bytes", request: PersonalCollectionUpdateRequest{QueryDefinition: json.RawMessage(`{"groups":[{"rules":[{"field":"title","op":"is","value":"` + strings.Repeat("x", 65528) + `"}]}]}`)}, display: string(compositionRules(t, 1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &compositionRecheckStore{lifecycleStore: lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", CollectionType: "smart", QueryDefinition: "{}"}}, lockedSource: tc.source, lockedDisplay: tc.display}
			h := NewCollectionHandler(pagingIntegrationProvider{account: 1, store: store})
			_, err := h.UpdatePersonalCollection(WithNativeAPIV2(t.Context()), PersonalCollectionUpdateCommand{UserID: 1, ProfileID: "owner", CollectionID: "c", Request: tc.request})
			assertCompositionFieldError(t, err)
			if !errors.Is(err, catalog.ErrQueryComplexity) {
				t.Errorf("raw recheck did not retain structural error: %v", err)
			}
		})
	}
}

func TestPersonalCollectionCompositionMetadataAndStoreErrors(t *testing.T) {
	for _, storeErr := range []error{nil, errors.New("synthetic database failure")} {
		store := &compositionRecheckStore{lifecycleStore: lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", CollectionType: "smart", QueryDefinition: string(compositionRules(t, 129))}}, storeError: storeErr}
		h := NewCollectionHandler(pagingIntegrationProvider{account: 1, store: store})
		_, err := h.UpdatePersonalCollection(WithNativeAPIV2(t.Context()), PersonalCollectionUpdateCommand{UserID: 1, ProfileID: "owner", CollectionID: "c", Request: PersonalCollectionUpdateRequest{Name: new("Synthetic renamed")}})
		if store.update.ValidateQueryComposition != nil {
			t.Error("legacy metadata edit installed definition validation")
		}
		if storeErr == nil && err != nil {
			t.Fatalf("legacy metadata edit: %v", err)
		}
		if storeErr != nil {
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != http.StatusInternalServerError {
				t.Fatalf("ordinary database error changed classification: %v", err)
			}
		}
	}
}
