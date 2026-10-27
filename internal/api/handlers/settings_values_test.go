package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func newValuesTestHandler(t *testing.T) (*SettingValuesHandler, userstore.UserStore) {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(),
		userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	return NewSettingValuesHandler(testUserStoreProvider{store: store}, contract), store
}

// valuesRequest builds a request carrying the session identity the handlers
// read: user, profile and device all come from context or headers rather than
// the query string, so one profile cannot address another's settings.
func valuesRequest(method, target string, body []byte) *http.Request {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	}
	req.Header.Set(deviceIDHeader, "device-1")
	req.Header.Set(clientFamilyHeader, "web")
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1})
	return req.WithContext(apimw.SetProfileID(ctx, "profile-1"))
}

type synchronizedShortcutStore struct {
	userstore.UserStore
	cas   userstore.SettingValueCompareAndSetter
	reads atomic.Int32
	ready chan struct{}
	once  sync.Once
	casMu sync.Mutex
}

func (s *synchronizedShortcutStore) GetSettingValue(ctx context.Context, id userstore.SettingIdentity) (*userstore.SettingValue, error) {
	value, err := s.UserStore.GetSettingValue(ctx, id)
	if err == nil && id.Key == settingskeys.NavShortcuts {
		read := s.reads.Add(1)
		if read <= 2 {
			if read == 2 {
				s.once.Do(func() { close(s.ready) })
			}
			<-s.ready
		}
	}
	return value, err
}

func (s *synchronizedShortcutStore) CompareAndSetSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
	expectedRevision int64,
) (*userstore.SettingValue, error) {
	s.casMu.Lock()
	defer s.casMu.Unlock()
	return s.cas.CompareAndSetSettingValue(ctx, id, value, expectedRevision)
}

func TestMutationHashPreservesExistingScopesAndSeparatesClientFamilies(t *testing.T) {
	identity := userstore.SettingIdentity{
		Key: "playback.subtitle_mode", Scope: settingscontract.ScopeProfileDevice,
		ProfileID: "profile-1", DeviceID: "device-1",
	}
	value := json.RawMessage(`"always"`)
	legacyPayload := append([]byte(
		"playback.subtitle_mode\x00profile_device\x00profile-1\x00device-1\x000\x00\x00",
	), value...)
	want := sha256.Sum256(legacyPayload)
	if got := hashMutationRequest(identity, value); got != hex.EncodeToString(want[:]) {
		t.Fatalf("existing-scope mutation hash changed across the profile_client rollout: %s", got)
	}

	client := userstore.SettingIdentity{
		Key: settingskeys.NavPrimaryMenu, Scope: settingscontract.ScopeProfileClient,
		ProfileID: "profile-1", ClientFamily: settingscontract.ClientFamilyTV,
	}
	tv := hashMutationRequest(client, json.RawMessage(`null`))
	client.ClientFamily = settingscontract.ClientFamilyMobile
	if mobile := hashMutationRequest(client, json.RawMessage(`null`)); mobile == tv {
		t.Fatal("profile_client mutation hashes do not distinguish client families")
	}
}

type settingValuesLibraryLookup struct {
	existing map[int]bool
	err      error
}

func (l settingValuesLibraryLookup) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	if l.err != nil {
		return nil, l.err
	}
	if !l.existing[id] {
		return nil, catalog.ErrFolderNotFound
	}
	return &models.MediaFolder{ID: id}, nil
}

// storedDeviceIDFor reports the device a profile_device row was written for, or
// "" when the key has no device-scoped row at all. The device-widening tests
// assert on stored rows rather than status codes: before the query parameter is
// honored a named device is silently ignored and the write lands on the header
// device, which is a 200 either way.
func storedDeviceIDFor(t *testing.T, store userstore.UserStore, key string) string {
	t.Helper()
	values, err := store.ListAllSettingValues(context.Background())
	if err != nil {
		t.Fatalf("listing stored values: %v", err)
	}
	for _, value := range values {
		if value.Key == key && value.Scope == settingscontract.ScopeProfileDevice {
			return value.DeviceID
		}
	}
	return ""
}

// --- Household widening: a primary profile addressing a sibling profile ---

// newHouseholdValuesHandler builds a handler with two profiles on one account:
// "profile-1" is the household parent, "profile-2" is another member.
func newHouseholdValuesHandler(t *testing.T, pin string) (*SettingValuesHandler, userstore.UserStore) {
	t.Helper()
	handler, store := newValuesTestHandler(t)

	// profile-1 is already the household parent: is_primary is assigned to the
	// first profile an account creates and is not settable afterwards.
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Robin"}); err != nil {
		t.Fatalf("create sibling: %v", err)
	}
	primary, err := store.GetProfile(ctx, "profile-1")
	if err != nil || primary == nil || !primary.IsPrimary {
		t.Fatalf("profile-1 is not the primary profile (%+v, %v)", primary, err)
	}
	if pin != "" {
		if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{
			PINHash: storedPIN(t, pin),
		}); err != nil {
			t.Fatalf("set pin: %v", err)
		}
	}

	registry := store.(userstore.DeviceRegistry)
	if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{
		ProfileID: "profile-2", DeviceID: "robin-ipad", DeviceName: "Robin's iPad",
	}); err != nil {
		t.Fatalf("registering sibling device: %v", err)
	}

	return handler, store
}

// captureAuditLogs swaps the default slog handler for the duration of a test.
// The returned function parses whatever has been emitted so far and keeps only
// the settings-audit records.
func captureAuditLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return func() []map[string]any {
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if record["msg"] == settingsAuditMsg {
				records = append(records, record)
			}
		}
		return records
	}
}

func TestMergeLanguageSuggestionsKeepsFloorObservedAndCurrent(t *testing.T) {
	got := mergeLanguageSuggestions(
		[]string{"en", "fr", "pt"},
		[]string{"eng", "deu", "not a tag", "fr"},
		json.RawMessage(`"pt-BR"`),
	)
	want := []string{"en", "fr", "pt", "de", "pt-BR"}
	if !slices.Equal(got, want) {
		t.Errorf("mergeLanguageSuggestions() = %v, want %v", got, want)
	}
}

func TestMergeLanguageSuggestionsUsesExactCurrentAlias(t *testing.T) {
	got := mergeLanguageSuggestions(
		[]string{"en", "fr"},
		[]string{"eng", "fra"},
		json.RawMessage(`"eng"`),
	)
	want := []string{"en", "fr"}
	if !slices.Equal(got, want) {
		t.Errorf("mergeLanguageSuggestions() = %v, want %v", got, want)
	}
}

type recordingLanguageSuggestionSource struct {
	filters  catalog.BrowseFilters
	original []string
	calls    int
}

func (s *recordingLanguageSuggestionSource) ListOriginalLanguages(
	_ context.Context, filters catalog.BrowseFilters,
) ([]string, error) {
	s.filters = filters
	s.calls++
	return s.original, nil
}

func TestObservedLanguageSuggestionsIncludesAccessibleOriginalLanguages(t *testing.T) {
	source := &recordingLanguageSuggestionSource{original: []string{"is", "no"}}
	handler, _ := newValuesTestHandler(t)
	handler.SetLanguageSuggestionSource(source)

	req := valuesRequest(http.MethodGet, "/settings/values/effective", nil)
	req = req.WithContext(access.SetScope(req.Context(), access.Scope{
		AllowedLibraryIDs:  []int{4, 9},
		DisabledLibraryIDs: []int{12},
		MaturityLimits:     access.MaturityLimits{MaxContentRating: "PG-13"},
	}))
	observed := handler.observedLanguageSuggestions(req.Context(), []settingsresolve.Effective{{
		Key: settingskeys.CatalogMetadataLanguage,
	}})

	if !slices.Equal(observed[settingskeys.CatalogMetadataLanguage], []string{"is", "no"}) {
		t.Fatalf("metadata suggestions = %v", observed[settingskeys.CatalogMetadataLanguage])
	}
	if !slices.Equal(source.filters.LibraryIDs, []int{4, 9}) ||
		!slices.Equal(source.filters.DisabledLibraryIDs, []int{12}) ||
		source.filters.MaxContentRating != "PG-13" {
		t.Fatalf("catalog filters = %#v", source.filters)
	}
}

// TestObservedLanguageSuggestionsSkipsPlaybackKeys pins the design decision
// that only catalog.metadata_language gets deployment-observed suggestions.
// The audio/subtitle track listings walk every media file — tens of seconds
// on large catalogs — so those pickers ship the contract floor and clients
// offer free entry for anything beyond it.
func TestObservedLanguageSuggestionsSkipsPlaybackKeys(t *testing.T) {
	source := &recordingLanguageSuggestionSource{original: []string{"is"}}
	handler, _ := newValuesTestHandler(t)
	handler.SetLanguageSuggestionSource(source)

	req := valuesRequest(http.MethodGet, "/settings/values/effective", nil)
	observed := handler.observedLanguageSuggestions(req.Context(), []settingsresolve.Effective{
		{Key: settingskeys.PlaybackAudioLanguage},
		{Key: settingskeys.PlaybackSubtitleLanguage},
	})

	if len(observed) != 0 {
		t.Fatalf("observed suggestions for playback keys = %v, want none", observed)
	}
	if source.calls != 0 {
		t.Fatalf("catalog scans = %d, want 0 for playback-only requests", source.calls)
	}
}

// TestV2SettingReadsBoundRepeatedValueBytes: v2 answers a repeated key once
// per mention, and the batch once per context too, so a large stored document
// must not be copied into one response without limit.
func TestV2SettingReadsBoundRepeatedValueBytes(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	ctx := t.Context()
	key := settingskeys.UiLibraryPageState
	// A schema-valid 128 KiB document: 32 libraries at the search length bound.
	libraries := make(map[string]map[string]string, 32)
	for i := range 32 {
		libraries[strconv.Itoa(i+1)] = map[string]string{"search": strings.Repeat("a", 4096)}
	}
	doc, err := json.Marshal(map[string]any{"version": 1, "libraries": libraries})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: key, Scope: settingscontract.ScopeProfileDevice, ProfileID: "profile-1", DeviceID: "device-1",
	}, doc); err != nil {
		t.Fatalf("seeding document: %v", err)
	}
	device := NewDeviceMetadata("device-1", "", "")
	contexts := make([]EffectiveContextRequest, 100)
	for i := range contexts {
		contexts[i] = EffectiveContextRequest{ContextID: strconv.Itoa(i), SeriesID: "s" + strconv.Itoa(i)}
	}
	batch := func(repeats int) error {
		_, err := handler.ResolveEffectiveSettingContexts(ctx, 1, EffectiveSettingsQuery{
			Keys: slices.Repeat([]string{key}, repeats), ActiveProfileID: "profile-1", Device: device,
		}, contexts)
		return err
	}
	list := func(repeats int) error {
		_, err := handler.ListSettingValues(ctx, 1, slices.Repeat([]string{key}, repeats), SettingIdentityRequest{
			Scope: string(settingscontract.ScopeProfileDevice), ActiveProfileID: "profile-1", Device: device,
		})
		return err
	}

	for name, tc := range map[string]struct {
		read     func(repeats int) error
		repeats  int
		rejected bool
	}{
		"batch within budget":         {read: batch, repeats: 1},
		"batch repeating past budget": {read: batch, repeats: 200, rejected: true},
		"list within budget":          {read: list, repeats: 100},
		"list repeating past budget":  {read: list, repeats: 200, rejected: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.read(tc.repeats)
			var apiErr *APIError
			rejected := errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest && apiErr.Field == settingFieldKeys
			if rejected != tc.rejected || (!rejected && err != nil) {
				t.Fatalf("err = %v, want rejected on keys = %v", err, tc.rejected)
			}
		})
	}
}

func TestNavigationShortcutMutationLifecycle(t *testing.T) {
	handler, _ := newValuesTestHandler(t)

	response, document := mustSetShortcut(t, handler, `{"type":"library","library_id":42,"label":"Movies"}`, true)
	if response.Key != settingskeys.NavShortcuts || response.Scope != "profile" || response.Revision != 1 {
		t.Fatalf("add response = %+v, want profile nav.shortcuts revision 1", response)
	}
	if len(document.Items) != 1 || document.Items[0].Label != "Movies" {
		t.Fatalf("add document = %+v, want Movies", document)
	}

	response, document = mustSetShortcut(t, handler, `{"type":"section","library_id":42,"section_id":"recent","label":"Recent"}`, true)
	if response.Revision != 2 || len(document.Items) != 2 || document.Items[1].SectionID != "recent" {
		t.Fatalf("add section = revision %d document %+v, want both items at revision 2", response.Revision, document)
	}

	refresh := `{"type":"library","library_id":42,"label":"Films"}`
	response, document = mustSetShortcut(t, handler, refresh, true)
	if response.Revision != 3 || len(document.Items) != 2 || document.Items[0].Label != "Films" || document.Items[1].SectionID != "recent" {
		t.Fatalf("label refresh reordered or dropped items: revision %d document %+v", response.Revision, document)
	}

	// Exact desired state is a no-op: no revision churn and no duplicate.
	response, document = mustSetShortcut(t, handler, refresh, true)
	if response.Revision != 3 || len(document.Items) != 2 {
		t.Fatalf("repeat add = revision %d document %+v, want unchanged revision 3", response.Revision, document)
	}

	// Removal matches semantic identity only; a stale label cannot stop it.
	remove := `{"type":"library","library_id":42,"label":"Old label"}`
	response, document = mustSetShortcut(t, handler, remove, false)
	if response.Revision != 4 || len(document.Items) != 1 || document.Items[0].SectionID != "recent" {
		t.Fatalf("remove library = revision %d document %+v, want section preserved", response.Revision, document)
	}
	if response, _ = mustSetShortcut(t, handler, remove, false); response.Revision != 4 {
		t.Fatalf("repeat remove revision = %d, want unchanged 4", response.Revision)
	}
}

func TestNavigationShortcutCollectionLibraryIDValidationCannotMutateExistingTargets(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	mustSetShortcut(t, handler, `{"type":"collection","collection_id":"favorites","label":"All Favorites"}`, true)
	mustSetShortcut(t, handler, `{"type":"collection","library_id":42,"collection_id":"favorites","label":"Movie Favorites"}`, true)

	for name, libraryID := range map[string]string{
		"explicit null": `null`,
		"string":        `"42"`,
		"zero":          `0`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := setShortcut(t, handler, fmt.Sprintf(
				`{"type":"collection","library_id":%s,"collection_id":"favorites","label":"Ignored"}`, libraryID), false)
			if apiErr := requireSettingStatus(t, err, http.StatusBadRequest, "invalid collection library_id"); apiErr.Code != settingErrorInvalidValue {
				t.Fatalf("code = %q, want %q", apiErr.Code, settingErrorInvalidValue)
			}

			stored, err := store.GetSettingValue(context.Background(), navShortcutIdentity())
			if err != nil || stored == nil {
				t.Fatalf("read catalog after rejected remove: value=%+v err=%v", stored, err)
			}
			var document navigationShortcutDocument
			if err := json.Unmarshal(stored.Value, &document); err != nil {
				t.Fatalf("decode catalog after rejected remove: %v", err)
			}
			if stored.Revision != 2 || len(document.Items) != 2 {
				t.Fatalf("rejected remove mutated catalog: revision %d items %+v", stored.Revision, document.Items)
			}
			if document.Items[0].LibraryID != nil {
				t.Fatalf("global collection gained library identity: %+v", document.Items[0])
			}
			if document.Items[1].LibraryID == nil || *document.Items[1].LibraryID != 42 {
				t.Fatalf("library-specific collection changed identity: %+v", document.Items[1])
			}
		})
	}

	response, document := mustSetShortcut(t, handler, `{"type":"collection","collection_id":"favorites","label":"Ignored"}`, false)
	if response.Revision != 3 || len(document.Items) != 1 || document.Items[0].LibraryID == nil || *document.Items[0].LibraryID != 42 {
		t.Fatalf("global removal = revision %d document %+v, want scoped collection preserved", response.Revision, document)
	}
}

func TestNavigationShortcutMutationValidationAndWholeDocumentGuard(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	ctx := seamCtx(t, "profile-1")

	_, err := handler.SetSettingValue(ctx, seamUserID,
		seamReq(settingskeys.NavShortcuts, settingscontract.ScopeProfile), json.RawMessage(`{"items":[]}`))
	if apiErr := requireSettingStatus(t, err, http.StatusBadRequest, "whole-document write"); apiErr.Code != settingErrorAtomicRequired {
		t.Fatalf("code = %q, want %q", apiErr.Code, settingErrorAtomicRequired)
	}
	if value, err := store.GetSettingValue(context.Background(), navShortcutIdentity()); err != nil || value != nil {
		t.Fatalf("guarded write stored %+v, err=%v", value, err)
	}

	for _, item := range []string{
		`{"type":"builtin","destination":"home","label":"Home"}`,
		`{"type":"library","library_id":42,"label":"Movies","extra":true}`,
		`{"type":"section","library_id":42,"section_id":"","label":"Recent"}`,
		`{"type":"collection","collection_id":"favorites","label":"   "}`,
		`{"type":"library","library_id":0,"label":"Movies"}`,
	} {
		if _, _, err := setShortcut(t, handler, item, true); err == nil {
			t.Errorf("invalid item %s was accepted", item)
		} else {
			requireSettingStatus(t, err, http.StatusBadRequest, "invalid item "+item)
		}
	}
}

func TestNavigationShortcutWholeDocumentDeleteCannotCreateRevisionABA(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	if first, _ := mustSetShortcut(t, handler, `{"type":"library","library_id":42,"label":"Movies"}`, true); first.Revision != 1 {
		t.Fatalf("seed revision = %d, want 1", first.Revision)
	}
	identity := navShortcutIdentity()
	beforeDelete, err := store.GetSettingValue(context.Background(), identity)
	if err != nil || beforeDelete == nil {
		t.Fatalf("read shortcut before guarded delete: value=%+v err=%v", beforeDelete, err)
	}

	err = handler.DeleteSettingValue(seamCtx(t, "profile-1"), seamUserID,
		seamReq(settingskeys.NavShortcuts, settingscontract.ScopeProfile))
	if apiErr := requireSettingStatus(t, err, http.StatusBadRequest, "whole-document delete"); apiErr.Code != settingErrorAtomicRequired {
		t.Fatalf("code = %q, want %q", apiErr.Code, settingErrorAtomicRequired)
	}

	if second, document := mustSetShortcut(t, handler, `{"type":"library","library_id":99,"label":"Shows"}`, true); second.Revision != 2 || len(document.Items) != 2 {
		t.Fatalf("second shortcut = revision %d document %+v", second.Revision, document)
	}

	// Had the delete removed and recreated the row, its revision would return
	// to one and this stale writer could pass an ABA-shaped precondition.
	cas := store.(userstore.SettingValueCompareAndSetter)
	if _, err := cas.CompareAndSetSettingValue(context.Background(), identity,
		beforeDelete.Value, beforeDelete.Revision); !errors.Is(err, userstore.ErrSettingValueRevisionConflict) {
		t.Fatalf("stale CAS after guarded delete error = %v, want revision conflict", err)
	}
}

func TestNavigationShortcutMutationEnforcesDocumentCap(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	items := make([]navigationShortcutItem, 0, 256)
	for id := 1; id <= 256; id++ {
		libraryID := id
		items = append(items, navigationShortcutItem{Type: "library", LibraryID: &libraryID, Label: fmt.Sprintf("Library %d", id)})
	}
	raw, err := json.Marshal(navigationShortcutDocument{Items: items})
	if err != nil {
		t.Fatal(err)
	}
	def, _ := handler.contract.Lookup(settingskeys.NavShortcuts)
	normalized, err := def.ValueSchema.NormalizeValue(raw, settingscontract.ObjectSchemas())
	if err != nil {
		t.Fatalf("normalize seed: %v", err)
	}
	seed, err := store.UpsertSettingValue(context.Background(), navShortcutIdentity(), normalized)
	if err != nil {
		t.Fatalf("seed full catalog: %v", err)
	}

	_, _, err = setShortcut(t, handler, `{"type":"library","library_id":257,"label":"Too Many"}`, true)
	if apiErr := requireSettingStatus(t, err, http.StatusBadRequest, "257th shortcut"); apiErr.Code != settingErrorInvalidValue {
		t.Fatalf("code = %q, want %q", apiErr.Code, settingErrorInvalidValue)
	}
	got, err := store.GetSettingValue(context.Background(), navShortcutIdentity())
	if err != nil || got == nil || got.Revision != seed.Revision {
		t.Fatalf("catalog after rejected add = %+v, err=%v; want unchanged revision %d", got, err, seed.Revision)
	}
}

func TestNavigationShortcutConcurrentAddsMerge(t *testing.T) {
	baseHandler, baseStore := newValuesTestHandler(t)
	wrapped := &synchronizedShortcutStore{
		UserStore: baseStore,
		cas:       baseStore.(userstore.SettingValueCompareAndSetter),
		ready:     make(chan struct{}),
	}
	handler := NewSettingValuesHandler(testUserStoreProvider{store: wrapped}, baseHandler.contract)
	items := []string{
		`{"type":"library","library_id":42,"label":"Movies"}`,
		`{"type":"library","library_id":99,"label":"Shows"}`,
	}

	errs := make(chan error, len(items))
	for _, item := range items {
		go func() {
			_, err := handler.SetNavigationShortcut(t.Context(), seamUserID, "profile-1", json.RawMessage(item), true)
			errs <- err
		}()
	}
	for range items {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}

	got, err := baseStore.GetSettingValue(context.Background(), navShortcutIdentity())
	if err != nil || got == nil {
		t.Fatalf("read merged catalog: value=%+v err=%v", got, err)
	}
	var document navigationShortcutDocument
	if err := json.Unmarshal(got.Value, &document); err != nil {
		t.Fatalf("decode merged catalog: %v", err)
	}
	if got.Revision != 2 || len(document.Items) != 2 {
		t.Fatalf("merged catalog = revision %d items %+v, want both adds at revision 2", got.Revision, document.Items)
	}
}

func TestSettingValuesRejectNonexistentLibraryContext(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	handler.SetLibraryLookup(settingValuesLibraryLookup{existing: map[int]bool{7: true}})
	ctx := seamCtx(t, "profile-1")
	req := seamReq("playback.subtitle_language", settingscontract.ScopeProfileLibrary)
	req.LibraryID = "99"

	_, err := handler.GetSettingValue(ctx, seamUserID, req)
	requireSettingStatus(t, err, http.StatusNotFound, "read at a nonexistent library")
	_, err = handler.SetSettingValue(ctx, seamUserID, req, json.RawMessage(`"de"`))
	requireSettingStatus(t, err, http.StatusNotFound, "write at a nonexistent library")
	err = handler.DeleteSettingValue(ctx, seamUserID, req)
	requireSettingStatus(t, err, http.StatusNotFound, "clear at a nonexistent library")

	value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key: "playback.subtitle_language", Scope: settingscontract.ScopeProfileLibrary,
		ProfileID: "profile-1", LibraryID: 99,
	})
	if err != nil || value != nil {
		t.Fatalf("nonexistent library left value (%+v, %v)", value, err)
	}

	req.LibraryID = "7"
	seamSet(t, ctx, handler, req, `"de"`)
}

// TestSettingWritesAreRefusedOutsideTheContract: the contract decides which
// keys the server stores, at which scopes, and in what shape. A refused write
// stores nothing.
func TestSettingWritesAreRefusedOutsideTheContract(t *testing.T) {
	for name, tc := range map[string]struct {
		key, value string
		scope      settingscontract.Scope
		seriesID   string
		libraryID  string
		wantStatus int
	}{
		// The legacy endpoint stored any unregistered key as an unvalidated
		// string, which is how untyped keys reached production.
		"unknown key":                         {key: "totally.invented.key", value: `"x"`, scope: settingscontract.ScopeProfile, wantStatus: http.StatusNotFound},
		"client-local key":                    {key: "downloads.wifi_only", value: `true`, scope: settingscontract.ScopeProfile, wantStatus: http.StatusBadRequest},
		"scope the definition does not allow": {key: "ui.custom_css", value: `"body{}"`, scope: settingscontract.ScopeProfileDevice, wantStatus: http.StatusBadRequest},
		"missing scope":                       {key: "playback.subtitle_mode", value: `"always"`, wantStatus: http.StatusBadRequest},
		"library scope without library_id":    {key: "playback.subtitle_language", value: `"de"`, scope: settingscontract.ScopeProfileLibrary, wantStatus: http.StatusBadRequest},
		"series scope without series_id":      {key: "playback.subtitle_language", value: `"de"`, scope: settingscontract.ScopeProfileSeries, wantStatus: http.StatusBadRequest},
		"value outside the schema":            {key: "playback.subtitle_mode", value: `"sideways"`, scope: settingscontract.ScopeProfile, wantStatus: http.StatusBadRequest},
		"library write":                       {key: "playback.subtitle_language", value: `"de"`, scope: settingscontract.ScopeProfileLibrary, libraryID: "7"},
		"series write":                        {key: "playback.subtitle_language", value: `"ja"`, scope: settingscontract.ScopeProfileSeries, seriesID: "s1"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, store := newValuesTestHandler(t)
			req := seamReq(tc.key, tc.scope)
			req.LibraryID, req.SeriesID = tc.libraryID, tc.seriesID
			_, err := handler.SetSettingValue(seamCtx(t, "profile-1"), seamUserID, req, json.RawMessage(tc.value))
			if tc.wantStatus == 0 {
				if err != nil {
					t.Fatalf("write: %v", err)
				}
				return
			}
			requireSettingStatus(t, err, tc.wantStatus, "refused write")
			if values, err := store.ListAllSettingValues(context.Background()); err != nil || len(values) != 0 {
				t.Fatalf("refused write stored %+v (%v)", values, err)
			}
		})
	}
}

// TestEffectiveResolutionRefusals: an effective read fails closed instead of
// returning a plausible wrong answer. An unknown key would let a client fill
// the gap with its own vendored default, and a device-aware key resolved
// without a device would pass the profile fallback off as effective.
func TestEffectiveResolutionRefusals(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	ctx := seamCtx(t, "profile-1")
	noDevice := func(keys ...string) EffectiveSettingsQuery {
		q := seamQuery(keys...)
		q.Device = DeviceMetadata{}
		return q
	}
	badFamily := seamQuery(settingskeys.UiCardPresentation)
	badFamily.ClientFamily = "TV"

	for name, tc := range map[string]struct {
		query      EffectiveSettingsQuery
		wantStatus int
	}{
		"unknown key":                     {query: seamQuery("playback.subtitle_mode", "totally.invented.key"), wantStatus: http.StatusNotFound},
		"device-aware key without device": {query: noDevice("playback.subtitle_language"), wantStatus: http.StatusBadRequest},
		"all keys without device":         {query: noDevice(), wantStatus: http.StatusBadRequest},
		"invalid client family":           {query: badFamily, wantStatus: http.StatusBadRequest},
		"profile-only key without device": {query: noDevice("ui.custom_css")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := handler.ResolveEffectiveSettings(ctx, seamUserID, tc.query)
			if tc.wantStatus == 0 {
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				return
			}
			apiErr := requireSettingStatus(t, err, tc.wantStatus, "refused resolve")
			if tc.wantStatus == http.StatusNotFound && !strings.Contains(apiErr.Message, "totally.invented.key") {
				t.Errorf("the error does not name the offending key: %s", apiErr.Message)
			}
		})
	}
}

func TestPostEffectiveRejectsInvalidContexts(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	for name, contexts := range map[string][]EffectiveContextRequest{
		"empty":           {},
		"duplicate id":    {{ContextID: "x", SeriesID: "s1"}, {ContextID: "x", SeriesID: "s2"}},
		"missing content": {{ContextID: "x"}},
		"invalid library": {{ContextID: "x", LibraryID: json.RawMessage(`"nope"`)}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := handler.ResolveEffectiveSettingContexts(seamCtx(t, "profile-1"), seamUserID,
				seamQuery("ui.custom_css"), contexts)
			requireSettingStatus(t, err, http.StatusBadRequest, "invalid contexts")
		})
	}
}

// TestEffectiveClientFamilyIsOptionalButValidated: an absent family drops the
// profile_client layer so older callers keep resolving broader fallbacks.
func TestEffectiveClientFamilyIsOptionalButValidated(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	ctx := seamCtx(t, "profile-1")
	key := settingskeys.UiCardPresentation
	seamSet(t, ctx, handler, seamReq(key, settingscontract.ScopeProfile), `{"poster_size":"compact","caption":"title"}`)
	seamSet(t, ctx, handler, seamReq(key, settingscontract.ScopeProfileClient), `{"poster_size":"large","caption":"artwork"}`)

	absent := seamQuery(key)
	absent.ClientFamily = ""
	if got := seamEffective(t, ctx, handler, absent); got.Source != string(settingscontract.ScopeProfile) ||
		string(got.Value) != `{"poster_size":"compact","caption":"title"}` {
		t.Fatalf("effective without family = %s from %s, want profile fallback", got.Value, got.Source)
	}
	all := seamQuery()
	all.ClientFamily = ""
	if _, err := handler.ResolveEffectiveSettings(ctx, seamUserID, all); err != nil {
		t.Fatalf("resolve all without client family: %v", err)
	}
	if got := seamEffective(t, ctx, handler, seamQuery(key)); got.Source != string(settingscontract.ScopeProfileClient) || got.ClientFamily != "web" {
		t.Fatalf("effective with family = %s/%s, want profile_client/web", got.Source, got.ClientFamily)
	}
}

// TestEffectiveAppliesViewerQualityCap: the access scope's MaxPlaybackQuality
// becomes the resolver's ceiling, so a capped viewer resolves the cap, and the
// authored preference survives untouched to take effect when the cap lifts.
func TestEffectiveAppliesViewerQualityCap(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	ctx := seamCtx(t, "profile-1")
	seamSet(t, ctx, handler, seamReq("playback.preferred_quality", settingscontract.ScopeProfile), `"2160p"`)

	effective := func(maxQuality string) EffectiveSettingValueView {
		t.Helper()
		scoped := access.SetScope(ctx, access.Scope{UserID: 1, ProfileID: "profile-1", MaxPlaybackQuality: maxQuality})
		return seamEffective(t, scoped, handler, seamQuery("playback.preferred_quality"))
	}

	got := effective("1080p")
	if string(got.Value) != `"1080p"` || !got.Constrained || got.ConstraintKind != string(settingscontract.ConstraintCeiling) {
		t.Errorf("capped effective = %s constrained=%v kind=%q, want \"1080p\"/true/ceiling",
			got.Value, got.Constrained, got.ConstraintKind)
	}
	if string(got.StoredValue) != `"2160p"` || string(got.RequestedValue) != `"2160p"` {
		t.Errorf("stored/requested = %s/%s, want the authored \"2160p\"", got.StoredValue, got.RequestedValue)
	}
	if got.ConstrainedBy == nil || got.ConstrainedBy.PolicyInput != policyInputMaxPlaybackQuality {
		t.Errorf("constrained_by = %#v", got.ConstrainedBy)
	}

	stored, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
		Key: "playback.preferred_quality", Scope: settingscontract.ScopeProfile, ProfileID: "profile-1",
	})
	if err != nil || stored == nil || string(stored.Value) != `"2160p"` {
		t.Fatalf("stored row = %+v (%v), want \"2160p\" untouched", stored, err)
	}

	if got = effective(""); string(got.Value) != `"2160p"` || got.Constrained || got.StoredValue != nil {
		t.Errorf("uncapped effective = %s constrained=%v stored=%s, want \"2160p\" unconstrained",
			got.Value, got.Constrained, got.StoredValue)
	}
}

// TestSettingAuthorityForOtherProfilesAndDevices: a request may name another
// profile only as the household parent, and another device only when it
// belongs to the addressed profile. Unknown ids answer 404 so the caller
// learns nothing about ids elsewhere.
func TestSettingAuthorityForOtherProfilesAndDevices(t *testing.T) {
	type call int
	const (
		set call = iota
		clearValue
		resolve
	)
	for name, tc := range map[string]struct {
		pin        string
		acting     string
		admin      bool
		profileID  string
		deviceID   string
		op         call
		wantStatus int
		wantMsg    string
		// wantRow is the (profile, device) a successful write lands on.
		wantRow [2]string
	}{
		"write to a device the profile does not own":    {acting: "profile-1", deviceID: "device-someone-else", wantStatus: http.StatusNotFound},
		"clear on a device the profile does not own":    {acting: "profile-1", deviceID: "device-someone-else", op: clearValue, wantStatus: http.StatusNotFound},
		"resolve a device the profile does not own":     {acting: "profile-1", deviceID: "not-mine", op: resolve, wantStatus: http.StatusNotFound},
		"non-primary names a sibling":                   {acting: "profile-2", profileID: "profile-1", deviceID: "device-1", wantStatus: http.StatusForbidden},
		"non-primary resolves a sibling":                {acting: "profile-2", profileID: "profile-1", op: resolve, wantStatus: http.StatusForbidden},
		"non-primary on an admin account":               {acting: "profile-2", admin: true, profileID: "profile-1", deviceID: "device-1", wantStatus: http.StatusForbidden},
		"primary with an unverified PIN":                {pin: "1234", acting: "profile-1", profileID: "profile-2", deviceID: "robin-ipad", wantStatus: http.StatusForbidden, wantMsg: "PIN"},
		"profile from another account":                  {acting: "profile-1", profileID: "someone-elses-profile", deviceID: "device-1", wantStatus: http.StatusNotFound},
		"primary names a sibling with a foreign device": {acting: "profile-1", profileID: "profile-2", deviceID: "not-robins-device", wantStatus: http.StatusNotFound},
		"primary writes a sibling's device":             {acting: "profile-1", profileID: "profile-2", deviceID: "robin-ipad", wantRow: [2]string{"profile-2", "robin-ipad"}},
		"anyone may name their own profile":             {acting: "profile-2", profileID: "profile-2", deviceID: "robin-ipad", wantRow: [2]string{"profile-2", "robin-ipad"}},
	} {
		t.Run(name, func(t *testing.T) {
			handler, store := newHouseholdValuesHandler(t, tc.pin)
			ctx := seamCtx(t, tc.acting)
			if tc.admin {
				ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: seamUserID, Role: "admin"})
			}
			const key = "player.hdr_enabled"
			req := seamReq(key, settingscontract.ScopeProfileDevice)
			req.ActiveProfileID, req.ProfileID, req.DeviceID = tc.acting, tc.profileID, tc.deviceID

			var err error
			switch tc.op {
			case set:
				_, err = handler.SetSettingValue(ctx, seamUserID, req, json.RawMessage(`false`))
			case clearValue:
				err = handler.DeleteSettingValue(ctx, seamUserID, req)
			case resolve:
				q := seamQuery("playback.subtitle_mode")
				q.ActiveProfileID, q.ProfileID, q.DeviceID = tc.acting, tc.profileID, tc.deviceID
				_, err = handler.ResolveEffectiveSettings(ctx, seamUserID, q)
			}

			if tc.wantStatus != 0 {
				apiErr := requireSettingStatus(t, err, tc.wantStatus, "refused call")
				if !strings.Contains(apiErr.Message, tc.wantMsg) {
					t.Errorf("message %q does not mention %q", apiErr.Message, tc.wantMsg)
				}
				if got := storedDeviceIDFor(t, store, key); got != "" {
					t.Errorf("refused call wrote a row on device %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			values, err := store.ListAllSettingValues(context.Background())
			if err != nil || len(values) != 1 || values[0].ProfileID != tc.wantRow[0] || values[0].DeviceID != tc.wantRow[1] {
				t.Fatalf("stored rows = %+v (%v), want one on %v", values, err, tc.wantRow)
			}
		})
	}
}

// TestSettingWritesRegisterOnlyTheCallersOwnDevice: registration means "this
// device is in use by this profile". A write aimed at another device, or made
// on another profile's behalf, must not file the actor's device anywhere.
func TestSettingWritesRegisterOnlyTheCallersOwnDevice(t *testing.T) {
	for name, tc := range map[string]struct {
		profileID, deviceID string
		checkProfile        string
	}{
		"another device of the same profile": {deviceID: "apple-tv", checkProfile: "profile-1"},
		"a sibling profile's device":         {profileID: "profile-2", deviceID: "robin-ipad", checkProfile: "profile-2"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, store := newHouseholdValuesHandler(t, "")
			registry := store.(userstore.DeviceRegistry)
			if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
				ProfileID: "profile-1", DeviceID: "apple-tv", DeviceName: "Apple TV",
			}); err != nil {
				t.Fatalf("registering apple-tv: %v", err)
			}
			req := seamReq("player.hdr_enabled", settingscontract.ScopeProfileDevice)
			req.ProfileID, req.DeviceID = tc.profileID, tc.deviceID
			seamSet(t, seamCtx(t, "profile-1"), handler, req, `false`)

			exists, err := registry.DeviceExists(context.Background(), tc.checkProfile, "device-1")
			if err != nil {
				t.Fatalf("DeviceExists: %v", err)
			}
			if exists {
				t.Errorf("registered the acting device-1 under %s", tc.checkProfile)
			}
		})
	}
}

func TestSetValue_AuditsCrossProfileWrite(t *testing.T) {
	handler, _ := newHouseholdValuesHandler(t, "")
	audited := captureAuditLogs(t)

	req := seamReq("playback.subtitle_mode", settingscontract.ScopeProfileDevice)
	req.ProfileID, req.DeviceID = "profile-2", "robin-ipad"
	seamSet(t, seamCtx(t, "profile-1"), handler, req, `"always"`)

	records := audited()
	if len(records) != 1 {
		t.Fatalf("emitted %d audit records, want 1: %+v", len(records), records)
	}
	record := records[0]
	if record["actor_profile_id"] != "profile-1" || record["target_profile_id"] != "profile-2" {
		t.Errorf("actor/target = %v/%v, want profile-1/profile-2",
			record["actor_profile_id"], record["target_profile_id"])
	}
	if record["setting_key"] != "playback.subtitle_mode" || record["device_id"] != "robin-ipad" {
		t.Errorf("key/device = %v/%v", record["setting_key"], record["device_id"])
	}
	// Identity only: the value must never reach an operator's log.
	for key, value := range record {
		if text, ok := value.(string); ok && text == "always" {
			t.Errorf("audit record leaked the value under %q", key)
		}
	}
}

// TestEffectiveContextsPreserveContextIDsAndRepeatedKeys: v2 echoes each
// context_id exactly as sent and answers a repeated key once per mention.
func TestEffectiveContextsPreserveContextIDsAndRepeatedKeys(t *testing.T) {
	handler, _ := newValuesTestHandler(t)
	keys := []string{"ui.custom_css", "ui.custom_css"}
	contexts := []EffectiveContextRequest{{ContextID: " row-1 ", SeriesID: "s1"}, {ContextID: "row-1", SeriesID: "s2"}}
	views, err := handler.ResolveEffectiveSettingContexts(t.Context(), seamUserID, EffectiveSettingsQuery{
		ActiveProfileID: "profile-1", Keys: keys,
	}, contexts)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("contexts = %d", len(views))
	}
	for i, view := range views {
		if view.ContextID != contexts[i].ContextID {
			t.Fatalf("context id = %q, want %q", view.ContextID, contexts[i].ContextID)
		}
		if len(view.Settings) != 2 || view.Settings[0].Key != keys[0] || view.Settings[1].Key != keys[1] {
			t.Fatalf("settings = %+v; want both requested keys in order", view.Settings)
		}
	}
}

func TestGetSettingValuesReportsSetAndUnsetAtOneScope(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key: "playback.subtitle_mode", Scope: settingscontract.ScopeProfile, ProfileID: "profile-1",
	}, json.RawMessage(`"always"`)); err != nil {
		t.Fatalf("seeding explicit value: %v", err)
	}

	views, err := handler.ListSettingValues(seamCtx(t, "profile-1"), seamUserID,
		[]string{"playback.subtitle_mode", "playback.subtitle_language"},
		seamReq("", settingscontract.ScopeProfile))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("values = %d, want 2", len(views))
	}
	if views[0].Key != "playback.subtitle_mode" || !views[0].IsSet || string(views[0].Value) != `"always"` {
		t.Errorf("stored entry = %+v", views[0])
	}
	if views[1].Key != "playback.subtitle_language" || views[1].IsSet || views[1].Value != nil {
		t.Errorf("unset entry = %+v", views[1])
	}
}
