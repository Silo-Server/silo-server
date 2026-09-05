package apiv2

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

// TestProfileHeaderRequiredMatchesGateChain: the documented X-Profile-Id
// requirement is the one the gate chain enforces. Only profile_scoped runs
// RequireProfile; acting_admin and permission_gated accept an absent header,
// as v1's RequireActingAdmin does, so they document it as optional.
func TestProfileHeaderRequiredMatchesGateChain(t *testing.T) {
	cases := map[Class]*bool{
		ClassPublic:          nil,
		ClassAuthenticated:   nil,
		ClassProfileScoped:   ptr(true),
		ClassActingAdmin:     ptr(false),
		ClassPermissionGated: ptr(false),
	}
	for class, want := range cases {
		t.Run(string(class), func(t *testing.T) {
			op := &Operation{Operation: huma.Operation{Method: http.MethodGet, Path: Prefix + "/x", OperationID: "getX"}, Class: class}
			documentDeclaration(op, nil)
			var param *huma.Param
			for _, p := range op.Parameters {
				if p.In == "header" && p.Name == profileHeader {
					param = p
				}
			}
			if want == nil {
				if param != nil {
					t.Fatalf("%s documents %s", class, profileHeader)
				}
				return
			}
			if param == nil {
				t.Fatalf("%s does not document %s", class, profileHeader)
			}
			if param.Required != *want {
				t.Fatalf("%s: Required = %v, want %v", class, param.Required, *want)
			}
			if param.Description == "" {
				t.Fatalf("%s: header has no description", class)
			}
		})
	}
}

// TestImpliedStatusesTable locks the per-shape problem statuses the listener
// really produces: 408 rides on every body (the body-read deadline applies to
// all of them), alongside 413 and 415; 404 rides on a path parameter and on
// every profile-resolving class (viewer access answers it for an unknown
// X-Profile-Id); 503 rides on a service-backed handler even when public; the
// gated statuses on every non-public class.
func TestImpliedStatusesTable(t *testing.T) {
	cases := []struct {
		name          string
		class         Class
		demo          bool
		serviceBacked bool
		body, path    bool
		want          []int
	}{
		{name: "public no body", class: ClassPublic, want: []int{400, 406, 422, 500}},
		{name: "public body", class: ClassPublic, body: true, want: []int{400, 406, 408, 413, 415, 422, 500}},
		{name: "public service backed", class: ClassPublic, serviceBacked: true, want: []int{400, 406, 422, 500, 503}},
		{name: "authenticated path", class: ClassAuthenticated, path: true, want: []int{400, 401, 404, 406, 422, 429, 500, 503}},
		{name: "authenticated demo body", class: ClassAuthenticated, demo: true, body: true, want: []int{400, 401, 403, 406, 408, 413, 415, 422, 429, 500, 503}},
		{name: "authenticated service backed", class: ClassAuthenticated, serviceBacked: true, want: []int{400, 401, 406, 422, 429, 500, 503}},
		{name: "profile scoped", class: ClassProfileScoped, want: []int{400, 401, 403, 404, 406, 422, 429, 500, 503}},
		{name: "profile scoped body path", class: ClassProfileScoped, body: true, path: true, want: []int{400, 401, 403, 404, 406, 408, 413, 415, 422, 429, 500, 503}},
		{name: "acting admin", class: ClassActingAdmin, want: []int{400, 401, 403, 404, 406, 422, 429, 500, 503}},
		{name: "permission gated", class: ClassPermissionGated, want: []int{400, 401, 403, 404, 406, 422, 429, 500, 503}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ImpliedStatuses(tc.class, tc.demo, tc.serviceBacked, tc.body, tc.path)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ImpliedStatuses = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDocumentDeclarationKeepsDeclaredErrors: a status the operation declares
// itself (updateProfile's 409) survives the class table being merged in, and
// the shared statuses join it.
func TestDocumentDeclarationKeepsDeclaredErrors(t *testing.T) {
	op := &Operation{Operation: huma.Operation{Method: http.MethodPatch, Path: Prefix + "/x/{id}", OperationID: "patchX", Errors: []int{http.StatusConflict}}, Class: ClassProfileScoped}
	documentDeclaration(op, reflect.TypeOf(ProfileUpdateInput{}))
	want := map[int]bool{http.StatusConflict: true, http.StatusRequestTimeout: true, http.StatusNotFound: true, http.StatusUnsupportedMediaType: true}
	for _, s := range op.Errors {
		delete(want, s)
	}
	if len(want) != 0 {
		t.Fatalf("errors %v lack %v", op.Errors, want)
	}
}

// generatedDocument decodes the generator's output for the tests that walk it.
func generatedDocument(t *testing.T) map[string]any {
	t.Helper()
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestGeneratedDocumentStatuses walks the generated artifact: updateProfile
// documents the 409 v1 answers a taken name with; every operation with a
// request body documents the 408 the body-read deadline produces; the
// service-backed public getSetupStatus documents the 503 its handler answers
// when the account service is not wired, while the discovery operations do
// not; and every profile-resolving operation documents the 404 viewer access
// answers for an unknown X-Profile-Id and the X-Profile-Token header a
// PIN-locked profile needs.
func TestGeneratedDocumentStatuses(t *testing.T) {
	doc := generatedDocument(t)
	bodies := 0
	expect := map[string]map[int]bool{
		"getSetupStatus":     {http.StatusServiceUnavailable: true},
		"getSystemInfo":      {http.StatusServiceUnavailable: false},
		"getOpenAPIDocument": {http.StatusServiceUnavailable: false},
		"getCurrentUser":     {http.StatusNotFound: false},
		"listProgress":       {http.StatusNotFound: true},
		"listAdminUsers":     {http.StatusNotFound: true},
		"updateProfile":      {http.StatusNotFound: true, http.StatusConflict: true},
		"createLibrary":      {http.StatusNotFound: true, http.StatusConflict: true, http.StatusCreated: true},
		"updateLibrary":      {http.StatusNotFound: true, http.StatusConflict: true},
		"deleteLibrary":      {http.StatusNotFound: true, http.StatusConflict: true, http.StatusAccepted: true},
		"setRootOverride":    {http.StatusNotFound: true, http.StatusConflict: true, http.StatusNoContent: true},
		"listLibraries":      {http.StatusNotFound: true, http.StatusConflict: false},
	}
	// Every operation the viewer-access gate fronts documents the header;
	// the acting-admin class resolves the declared profile the same way.
	profileToken := map[string]bool{"listProgress": true, "listAdminUsers": true, "updateProfile": true}
	for _, id := range libraryOperationIDs {
		profileToken[id] = true
	}
	for _, id := range libraryViewOperationIDs {
		profileToken[id] = true
	}
	for _, id := range []string{"listFavorites", "getFavorite", "addFavorite", "deleteFavorite", "listRatings", "getRating", "setRating", "deleteRating", "listWatchlist", "getWatchlistEntry", "addToWatchlist", "deleteWatchlistEntry"} {
		profileToken[id] = true
	}
	expect["refreshLibraryMetadata"] = map[int]bool{http.StatusNotFound: true, http.StatusConflict: true, http.StatusAccepted: true}
	expect["uploadLibraryPoster"] = map[int]bool{http.StatusNotFound: true, http.StatusRequestEntityTooLarge: true, http.StatusUnsupportedMediaType: true}
	expect["getLibraryLayout"] = map[int]bool{http.StatusNotFound: true, http.StatusConflict: false}
	seen := map[string]bool{}
	for path, item := range doc["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			op := raw.(map[string]any)
			responses := op["responses"].(map[string]any)
			id, _ := op["operationId"].(string)
			seen[id] = true
			for status, want := range expect[id] {
				if _, ok := responses[strconv.Itoa(status)]; ok != want {
					t.Errorf("%s documents %d = %v, want %v", id, status, ok, want)
				}
			}
			if got := declaresHeader(op, profileTokenHeader); got != profileToken[id] {
				t.Errorf("%s documents %s = %v, want %v", id, profileTokenHeader, got, profileToken[id])
			}
			if op["requestBody"] == nil {
				continue
			}
			bodies++
			if _, ok := responses[strconv.Itoa(http.StatusRequestTimeout)]; !ok {
				t.Errorf("%s %s has a body but does not document 408", method, path)
			}
		}
	}
	if bodies == 0 {
		t.Fatal("no operation with a request body; the 408 rule is untested")
	}
	for id := range expect {
		if !seen[id] {
			t.Errorf("operation %s is not in the generated document", id)
		}
	}
}

// declaresHeader reports whether the decoded operation documents the header
// parameter, and requires it to be optional with a description: the token
// is proof for a locked profile, never a requirement of its own.
func declaresHeader(op map[string]any, name string) bool {
	params, _ := op["parameters"].([]any)
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		if p["in"] == "header" && p["name"] == name {
			required, _ := p["required"].(bool)
			desc, _ := p["description"].(string)
			return !required && desc != ""
		}
	}
	return false
}

// TestGeneratedDocumentRequestMediaTypes: no operation documents a request
// media type mediaTypeGuard would answer with 415. Huma documents a RawBody
// as application/octet-stream; updateProfile carries one only for its
// omitted-versus-null rule and accepts application/json alone.
func TestGeneratedDocumentRequestMediaTypes(t *testing.T) {
	doc := generatedDocument(t)
	bodies := 0
	for path, item := range doc["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			op := raw.(map[string]any)
			body, ok := op["requestBody"].(map[string]any)
			if !ok {
				continue
			}
			bodies++
			content := body["content"].(map[string]any)
			if len(content) == 0 {
				t.Errorf("%s %s documents a body with no media type", method, path)
			}
			for mediaType := range content {
				if structuredMediaTypeOK(mediaType) {
					continue
				}
				// A multipart operation takes multipart only (mediaTypeGuard).
				if mediaType == mediaTypeMultipart && len(content) == 1 {
					continue
				}
				t.Errorf("%s %s documents request media type %q that the listener rejects with 415", method, path, mediaType)
			}
		}
	}
	if bodies == 0 {
		t.Fatal("no operation with a request body; the media-type rule is untested")
	}
}

// libraryOperationIDs is every acting-admin library operation the
// catalog-libraries section registers.
var libraryOperationIDs = []string{
	"listLibraries", "createLibrary", "updateLibrary", "deleteLibrary", "checkLibraryMount",
	"listMetadataMatchQueues", "getLibraryProviderDefaults", "reorderLibraries", "listLibraryRoots",
	"setRootOverride", "deleteRootOverride", "listSkippedRoots", "listStaleIds", "rematchStaleId", "listUnmatchedItems",
	"confirmEmptyRootCleanup", "getMetadataMatchQueue", "retryMetadataMatchQueue", "cancelMetadataMatchQueue", "refreshLibraryMetadata",
	"getLibraryProviders", "setLibraryProviders", "uploadLibraryPoster", "deleteLibraryPoster",
}

// libraryViewOperationIDs is every profile-scoped library read the
// catalog-libraries section registers.
var libraryViewOperationIDs = []string{
	"getLibraryLayout", "listLibrarySections", "getLibrarySectionItems", "getLibraryCollections", "getLibraryCollectionItems", "listLibraryUserCollections",
}
