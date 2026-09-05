package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
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
			got := ImpliedStatuses(tc.class, tc.demo, tc.serviceBacked, tc.body, tc.path, false, false, false)
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
		"getSetupStatus":                 {http.StatusServiceUnavailable: true},
		"getSystemInfo":                  {http.StatusServiceUnavailable: false},
		"getOpenAPIDocument":             {http.StatusServiceUnavailable: false},
		"getCurrentUser":                 {http.StatusNotFound: false},
		"listProgress":                   {http.StatusNotFound: true},
		"listAdminUsers":                 {http.StatusNotFound: true},
		"updateProfile":                  {http.StatusNotFound: true, http.StatusConflict: true},
		"listProfiles":                   {http.StatusNotFound: true, http.StatusConflict: false},
		"createProfile":                  {http.StatusNotFound: true, http.StatusConflict: true, http.StatusCreated: true, http.StatusOK: false},
		"replaceProfileSectionOverrides": {http.StatusNoContent: true, http.StatusForbidden: true},
		"resetProfileSectionOverrides":   {http.StatusNoContent: true},
		"deleteProfile":                  {http.StatusNoContent: true, http.StatusConflict: true, http.StatusNotFound: true},
		"deleteProfileAvatar":            {http.StatusNoContent: true, http.StatusNotFound: true},
		"uploadProfileAvatar":            {http.StatusOK: true, http.StatusRequestEntityTooLarge: true, http.StatusUnsupportedMediaType: true},
		"verifyProfilePIN":               {http.StatusOK: true, http.StatusNotFound: true},
		"listHouseholdSessions":          {http.StatusOK: true, http.StatusForbidden: true},
	}
	profileToken := map[string]bool{
		"listProgress": true, "listAdminUsers": true, "updateProfile": true, "listProfiles": true, "createProfile": true,
		"listProfileSectionOverrides": true, "replaceProfileSectionOverrides": true, "resetProfileSectionOverrides": true,
		"getProfileSectionSettings": true, "getProfileSectionFlags": true,
		"deleteProfile": true, "deleteProfileAvatar": true, "uploadProfileAvatar": true, "verifyProfilePIN": true, "listHouseholdSessions": true,
		"getAudioPreference": true, "updateAudioPreference": true, "deleteAudioPreference": true,
		"listLibraryPlaybackPreferences": true, "deleteLibraryPlaybackPreference": true, "updateLibraryPlaybackPreference": true,
		"getSubtitlePreference": true, "updateSubtitlePreference": true, "deleteSubtitlePreference": true,
	}
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
			if got := documentsHeaderParam(op, profileTokenHeader); got != profileToken[id] {
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

// documentsHeaderParam reports whether the decoded operation documents the header
// parameter, and requires it to be optional with a description: the token
// is proof for a locked profile, never a requirement of its own.
func documentsHeaderParam(op map[string]any, name string) bool {
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
				if !requestMediaTypeOK(mediaType) {
					t.Errorf("%s %s documents request media type %q that the listener rejects with 415", method, path, mediaType)
				}
			}
		}
	}
	if bodies == 0 {
		t.Fatal("no operation with a request body; the media-type rule is untested")
	}
}

func TestImpliedStatusesForConcurrency(t *testing.T) {
	has := func(s []int, v int) bool {
		for _, x := range s {
			if x == v {
				return true
			}
		}
		return false
	}
	plain := ImpliedStatuses(ClassPublic, false, false, false, true, false, false, false)
	if has(plain, http.StatusPreconditionFailed) || has(plain, http.StatusPreconditionRequired) || has(plain, http.StatusNotModified) {
		t.Fatalf("plain operation implies concurrency statuses: %v", plain)
	}
	guarded := ImpliedStatuses(ClassPublic, false, false, true, true, true, false, false)
	if !has(guarded, http.StatusPreconditionFailed) || !has(guarded, http.StatusPreconditionRequired) || has(guarded, http.StatusNotModified) {
		t.Fatalf("guarded = %v", guarded)
	}
	conditional := ImpliedStatuses(ClassPublic, false, false, false, true, false, true, false)
	if !has(conditional, http.StatusNotModified) || !has(conditional, http.StatusPreconditionFailed) || has(conditional, http.StatusPreconditionRequired) {
		t.Fatalf("conditional = %v", conditional)
	}
	createOnly := ImpliedStatuses(ClassPublic, false, false, true, true, false, false, true)
	if !has(createOnly, http.StatusPreconditionFailed) || has(createOnly, http.StatusPreconditionRequired) || has(createOnly, http.StatusNotModified) {
		t.Fatalf("createOnly = %v", createOnly)
	}
}

// conditionalDocOutput is the minimal output shape a Conditional read needs.
type conditionalDocOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   probeEcho
}

type guardedDocOutput struct {
	ETag string `header:"ETag"`
	Body probeEcho
}

// validatorHeaders is an embedded shape a domain package might be tempted to
// share between outputs. Huma writes no header from an embedded struct, so
// Register refuses it: the ETag must be a direct field.
type validatorHeaders struct {
	ETag string `header:"ETag"`
}

type embeddedConditionalDocOutput struct {
	Status int
	validatorHeaders
	Body probeEcho
}

func registerConcurrencyDocProbes(reg *Registry) {
	current := RenderETag("doc", "a", 1)
	Register(reg, Operation{
		Operation: func() huma.Operation {
			o := humaOp(http.MethodGet, Prefix+"/docprobe/{id}", "getDocProbe", "probe", "conditional")
			// A registration may declare its own 304 (a polled job adds
			// Retry-After); documentConcurrencyResponses merges ETag into
			// it rather than replacing it.
			o.Responses = map[string]*huma.Response{"304": {
				Description: "Still current; poll again after Retry-After.",
				// Content on a 304 can never be sent; the merge must clear it.
				Content: map[string]*huma.MediaType{"application/json": {}},
				// A lower-case etag key: the merge must fold it into the
				// canonical entry rather than add a second one.
				// Two case-equivalent keys, one with the wrong schema: both
				// must collapse into one canonical string-schema entry that
				// keeps the declared description.
				Headers: map[string]*huma.Header{"Retry-After": {Schema: &huma.Schema{Type: "integer"}}, "etag": {Description: "declared by the registration", Schema: &huma.Schema{Type: "integer"}}, "ETAG": {Schema: &huma.Schema{Type: "integer"}}},
			}}
			return o
		}(),
		Class:       ClassPublic,
		Conditional: true,
	}, func(_ context.Context, in *struct {
		ID          string `path:"id"`
		IfMatch     string `header:"If-Match"`
		IfNoneMatch string `header:"If-None-Match"`
	}) (*conditionalDocOutput, error) {
		out := &conditionalDocOutput{ETag: current.String(), Body: probeEcho{Name: in.ID, Tags: []string{}, Labels: map[string]int{}}}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, current); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, current), nil
		}
		return out, nil
	})
	Register(reg, Operation{
		Operation: func() huma.Operation {
			o := humaOp(http.MethodPut, Prefix+"/docprobe/{id}", "putDocProbe", "probe", "guarded")
			// A hand-declared 2XX range is a success too and must document
			// the ETag the runtime sends.
			o.Responses = map[string]*huma.Response{"2XX": {Description: "any success"}}
			return o
		}(),
		Class:   ClassPublic,
		Guarded: true,
	}, func(_ context.Context, in *struct {
		ID          string `path:"id"`
		IfMatch     string `header:"If-Match"`
		IfNoneMatch string `header:"If-None-Match"`
		Body        probeBody
	}) (*guardedDocOutput, error) {
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, current); p != nil {
			return nil, p
		}
		return &guardedDocOutput{ETag: current.String(), Body: probeEcho{Name: in.Body.Name, Tags: []string{}, Labels: map[string]int{}}}, nil
	})
	Register(reg, Operation{
		Operation: func() huma.Operation {
			o := humaOp(http.MethodPatch, Prefix+"/docprobe/{id}", "patchDocProbe", "probe", "guarded bodyless")
			// A hand-declared 204 that names a representation: the runtime
			// never sends a 204 body, so the document must not promise one.
			o.Responses = map[string]*huma.Response{"204": {
				Description: "updated",
				Content:     map[string]*huma.MediaType{"application/json": {Schema: &huma.Schema{Type: huma.TypeObject}}},
			}}
			return o
		}(),
		Class:   ClassPublic,
		Guarded: true,
	}, func(_ context.Context, in *struct {
		ID          string `path:"id"`
		IfMatch     string `header:"If-Match"`
		IfNoneMatch string `header:"If-None-Match"`
		Body        probeBody
	}) (*struct {
		ETag string `header:"ETag"`
	}, error) {
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, current); p != nil {
			return nil, p
		}
		return &struct {
			ETag string `header:"ETag"`
		}{ETag: current.String()}, nil
	})
	Register(reg, Operation{
		Operation: humaOp(http.MethodDelete, Prefix+"/docprobe/{id}", "deleteDocProbe", "probe", "guarded delete"),
		Class:     ClassPublic,
		Guarded:   true,
	}, func(_ context.Context, in *struct {
		ID          string `path:"id"`
		IfMatch     string `header:"If-Match"`
		IfNoneMatch string `header:"If-None-Match"`
	}) (*struct{}, error) {
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, current); p != nil {
			return nil, p
		}
		return &struct{}{}, nil
	})
	Register(reg, Operation{
		Operation:  humaOp(http.MethodPut, Prefix+"/docprobe/created/{id}", "putCreatedDocProbe", "probe", "create-only"),
		Class:      ClassPublic,
		CreateOnly: true,
	}, func(_ context.Context, in *struct {
		ID          string `path:"id"`
		IfMatch     string `header:"If-Match"`
		IfNoneMatch string `header:"If-None-Match"`
		Body        probeBody
	}) (*guardedDocOutput, error) {
		if p := EvaluateCreateOnlyPreconditions(in.IfMatch, in.IfNoneMatch, &current); p != nil {
			return nil, p
		}
		return &guardedDocOutput{ETag: current.String(), Body: probeEcho{Name: in.Body.Name, Tags: []string{}, Labels: map[string]int{}}}, nil
	})
}

// TestConcurrencyDeclarationsAreDocumented checks the document a guarded and a
// conditional registration produce: the extension, the required If-Match
// parameter, the ETag header on every 2xx, the 304 response, and the
// implied problem statuses.
func TestConcurrencyDeclarationsAreDocumented(t *testing.T) {
	api := huma.NewAPI(humaConfig(), noopAdapter{})
	reg := &Registry{api: api}
	registerConcurrencyDocProbes(reg)
	raw, err := api.OpenAPI().MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Extensions map[string]any
			Parameters []struct {
				Name     string `json:"name"`
				In       string `json:"in"`
				Required bool   `json:"required"`
			} `json:"parameters"`
			Responses map[string]struct {
				Headers map[string]any `json:"headers"`
				Content map[string]any `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	ext := func(method, name string) any {
		return generic["paths"].(map[string]any)[Prefix+"/docprobe/{id}"].(map[string]any)[method].(map[string]any)[name]
	}
	put := doc.Paths[Prefix+"/docprobe/{id}"]["put"]
	if ext("put", extGuarded) != true || ext("put", extConditional) != nil {
		t.Fatalf("put extensions: guarded=%v conditional=%v", ext("put", extGuarded), ext("put", extConditional))
	}
	ifMatch := false
	for _, p := range put.Parameters {
		if p.Name == "If-Match" && p.In == "header" {
			ifMatch = p.Required
		}
	}
	if !ifMatch {
		t.Fatalf("If-Match is not a required header parameter: %+v", put.Parameters)
	}
	for _, status := range []string{"412", "428"} {
		if _, ok := put.Responses[status]; !ok {
			t.Fatalf("put lacks %s: %v", status, put.Responses)
		}
	}
	if put.Responses["200"].Headers["ETag"] == nil {
		t.Fatalf("put 200 lacks the ETag header: %+v", put.Responses["200"])
	}
	patch := doc.Paths[Prefix+"/docprobe/{id}"]["patch"]
	if r, ok := patch.Responses["204"]; !ok {
		t.Fatalf("patch lacks its declared 204: %v", patch.Responses)
	} else if len(r.Content) != 0 {
		t.Fatalf("patch 204 documents content the runtime never sends: %v", r.Content)
	} else if r.Headers["ETag"] == nil {
		t.Fatalf("patch 204 lacks the ETag header: %+v", r)
	}
	ifNoneMatchOnPut := false
	for _, p := range put.Parameters {
		if p.Name == "If-None-Match" && p.In == "header" {
			ifNoneMatchOnPut = true
			if p.Required {
				t.Fatal("If-None-Match on a guarded put must be optional")
			}
		}
	}
	if !ifNoneMatchOnPut {
		t.Fatalf("a guarded put that binds If-None-Match must document it: %+v", put.Parameters)
	}
	if put.Responses["2XX"].Headers["ETag"] == nil {
		t.Fatalf("a hand-declared 2XX range on a guarded put must document ETag: %+v", put.Responses["2XX"])
	}
	if put.Responses["412"].Headers["ETag"] == nil {
		t.Fatalf("put 412 lacks the ETag header a stale tag is answered with: %+v", put.Responses["412"])
	}
	if put.Responses["428"].Headers["ETag"] != nil {
		t.Fatalf("put 428 documents an ETag it never sends: %+v", put.Responses["428"])
	}
	get := doc.Paths[Prefix+"/docprobe/{id}"]["get"]
	if ext("get", extConditional) != true || ext("get", extGuarded) != nil {
		t.Fatalf("get extensions: guarded=%v conditional=%v", ext("get", extGuarded), ext("get", extConditional))
	}
	if get.Responses["200"].Headers["ETag"] == nil || get.Responses["304"].Headers["ETag"] == nil || len(get.Responses["304"].Content) != 0 {
		t.Fatalf("get responses (a predeclared 304 must lose its content): %+v", get.Responses)
	}
	if get.Responses["304"].Headers["Retry-After"] == nil {
		t.Fatalf("a pre-declared 304 lost its own header: %+v", get.Responses["304"])
	}
	etagKeys := 0
	for name := range get.Responses["304"].Headers {
		if strings.EqualFold(name, "ETag") {
			etagKeys++
		}
	}
	if etagKeys != 1 || get.Responses["304"].Headers["ETag"] == nil {
		t.Fatalf("declared case-variant etag headers must merge into one canonical ETag entry: %+v", get.Responses["304"].Headers)
	}
	if h := get.Responses["304"].Headers["ETag"].(map[string]any); h["schema"].(map[string]any)["type"] != "string" || h["description"] != "declared by the registration" {
		t.Fatalf("merged ETag must carry the contract's string schema and the declared description: %+v", h)
	}
	if r, ok := get.Responses["412"]; !ok || r.Headers["ETag"] == nil {
		t.Fatalf("a conditional read documents 412 with ETag for a stale If-Match: %+v", get.Responses["412"])
	}
	ifMatchOnGet := false
	for _, p := range get.Parameters {
		if p.Name == "If-Match" && p.In == "header" {
			ifMatchOnGet = true
			if p.Required {
				t.Fatal("If-Match on a conditional read must be optional")
			}
		}
	}
	if !ifMatchOnGet {
		t.Fatalf("a conditional read must document the optional If-Match: %+v", get.Parameters)
	}
	del := doc.Paths[Prefix+"/docprobe/{id}"]["delete"]
	if del.Responses["204"].Headers["ETag"] != nil {
		t.Fatalf("a 204 has no representation to validate, yet documents ETag: %+v", del.Responses["204"])
	}
	if _, ok := del.Responses["428"]; !ok {
		t.Fatalf("guarded delete lacks 428: %v", del.Responses)
	}
	created := doc.Paths[Prefix+"/docprobe/created/{id}"]["put"]
	if generic["paths"].(map[string]any)[Prefix+"/docprobe/created/{id}"].(map[string]any)["put"].(map[string]any)[extCreateOnly] != true {
		t.Fatal("create-only put lacks x-silo-create-only")
	}
	ifNoneMatch := false
	for _, p := range created.Parameters {
		if p.Name == "If-None-Match" && p.In == "header" {
			ifNoneMatch = true
			if p.Required {
				t.Fatal("If-None-Match on a create-only put must be optional")
			}
		}
	}
	if !ifNoneMatch {
		t.Fatalf("If-None-Match is not documented on the create-only put: %+v", created.Parameters)
	}
	if r, ok := created.Responses["412"]; !ok || r.Headers["ETag"] == nil {
		t.Fatalf("create-only 412 must be documented with the existing ETag: %+v", created.Responses["412"])
	}
	if _, ok := created.Responses["412"]; !ok {
		t.Fatalf("create-only put lacks 412: %v", created.Responses)
	}
	if _, ok := created.Responses["428"]; ok {
		t.Fatal("a create-only put must not document 428; If-None-Match is optional")
	}
	if created.Responses["200"].Headers["ETag"] == nil {
		t.Fatalf("create-only put 200 lacks the ETag header: %+v", created.Responses["200"])
	}
	if findings := lintExtensions(raw); len(findings) != 0 {
		t.Fatal(findings)
	}
}

// lintExtensions is a stand-in for internal/contractspec, which cannot be
// imported here: it checks the concurrency extensions round-trip as booleans.
func lintExtensions(raw []byte) []string {
	var d struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return []string{err.Error()}
	}
	var out []string
	for path, methods := range d.Paths {
		for method, op := range methods {
			for _, name := range []string{extGuarded, extConditional, extCreateOnly} {
				if v, ok := op[name]; ok && v != true {
					out = append(out, path+" "+method+" "+name+" is not true")
				}
			}
		}
	}
	return out
}

// TestNotModifiedHasNoBody proves through the real router that a conditional
// read answering 304 sends the ETag, no body and no Content-Type, and that
// the same read without a matching If-None-Match is a normal 200.
func TestNotModifiedHasNoBody(t *testing.T) {
	h := NewHandler(Dependencies{testRegister: registerConcurrencyDocProbes})
	current := RenderETag("doc", "a", 1)
	rec := do(t, h, http.MethodGet, Prefix+"/docprobe/a", "", map[string]string{"If-None-Match": current.String()})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 carries a body: %q", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != current.String() {
		t.Fatalf("ETag = %q, want %q", got, current.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Fatalf("304 carries Content-Type %q", ct)
	}
	if requestIDHeader(rec) == "" {
		t.Fatal("304 lacks X-Request-ID")
	}
	rec = do(t, h, http.MethodGet, Prefix+"/docprobe/a", "", map[string]string{"If-None-Match": RenderETag("doc", "a", 2).String()})
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 || rec.Header().Get("ETag") != current.String() {
		t.Fatalf("non-matching read: %d %q etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
}

// TestRegisterRefusesBadConcurrencyDeclarations: the shape rules are build
// failures, not request failures.
func TestRegisterRefusesBadConcurrencyDeclarations(t *testing.T) {
	type okIn struct {
		IfMatch     string `header:"If-Match"`
		IfNoneMatch string `header:"If-None-Match"`
	}
	type noHeaders struct{}
	type okOut struct {
		Status int
		ETag   string `header:"ETag"`
	}
	type noETag struct{ Status int }
	type noStatus struct {
		ETag string `header:"ETag"`
	}
	type intETag struct {
		Status int
		ETag   int `header:"ETag"`
	}
	type unexportedIn struct {
		ifMatch string `header:"If-Match"` //nolint:unused // the refusal is the point
	}
	type unexportedOut struct {
		Status int
		etag   string `header:"ETag"` //nolint:unused // the refusal is the point
	}
	type unexportedStatus struct {
		status int    //nolint:unused // the refusal is the point
		ETag   string `header:"ETag"`
	}
	guarded := func(method string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/x/{id}", "opX", "x", ""), Class: ClassPublic, Guarded: true}
	}
	conditional := func(method string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/x/{id}", "opX", "x", ""), Class: ClassPublic, Conditional: true}
	}
	createOnly := func(method string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/x/{id}", "opX", "x", ""), Class: ClassPublic, CreateOnly: true}
	}
	cases := map[string]struct {
		op   Operation
		reg  func(*Registry, Operation)
		want string
	}{
		"guarded GET": {guarded(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*okOut, error) { return nil, nil })
		}, "guarded is for PUT"},
		"guarded POST": {guarded(http.MethodPost), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*okOut, error) { return nil, nil })
		}, "guarded is for PUT"},
		"conditional PUT": {conditional(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*okOut, error) { return nil, nil })
		}, "conditional is for GET"},
		"create-only POST": {createOnly(http.MethodPost), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*okOut, error) { return nil, nil })
		}, "create-only is for PUT"},
		"create-only and guarded": {func() Operation { op := createOnly(http.MethodPut); op.Guarded = true; return op }(), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*okOut, error) { return nil, nil })
		}, "exclusive"},
		"guarded with non-string If-None-Match": {guarded(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *struct {
				IfMatch     string `header:"If-Match"`
				IfNoneMatch int    `header:"If-None-Match"`
			}) (*okOut, error) {
				return nil, nil
			})
		}, "If-None-Match"},
		"guarded without If-None-Match": {guarded(http.MethodPatch), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *struct {
				IfMatch string `header:"If-Match"`
			}) (*okOut, error) {
				return nil, nil
			})
		}, "If-None-Match"},
		"conditional with a second typed ETag": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*struct {
				Status int
				ETag   string `header:"ETag"`
				Other  int    `header:"ETag"`
			}, error) {
				return nil, nil
			})
		}, "ETag"},
		"guarded DELETE with DefaultStatus 200": {func() Operation { op := guarded(http.MethodDelete); op.DefaultStatus = http.StatusOK; return op }(), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noHeaders, error) { return nil, nil })
		}, "DefaultStatus"},
		"guarded DELETE with a declared 202": {func() Operation {
			op := guarded(http.MethodDelete)
			op.Responses = map[string]*huma.Response{"202": {Description: "accepted"}}
			return op
		}(), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noHeaders, error) { return nil, nil })
		}, "Responses declares 202"},
		"guarded DELETE with a declared 2XX range": {func() Operation {
			op := guarded(http.MethodDelete)
			op.Responses = map[string]*huma.Response{"2XX": {Description: "any success"}}
			return op
		}(), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noHeaders, error) { return nil, nil })
		}, "2XX range"},
		"guarded DELETE with 204 content": {func() Operation {
			op := guarded(http.MethodDelete)
			op.Responses = map[string]*huma.Response{"204": {Description: "gone", Content: map[string]*huma.MediaType{"application/json": {}}}}
			return op
		}(), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noHeaders, error) { return nil, nil })
		}, "declares content"},
		"guarded DELETE with 204 etag header": {func() Operation {
			op := guarded(http.MethodDelete)
			op.Responses = map[string]*huma.Response{"204": {Description: "gone", Headers: map[string]*huma.Header{"etag": {Schema: &huma.Schema{Type: "string"}}}}}
			return op
		}(), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noHeaders, error) { return nil, nil })
		}, "declares an ETag header"},
		"conditional with lower-case etag tag": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*struct {
				Status int
				ETag   string `header:"etag"`
			}, error) {
				return nil, nil
			})
		}, "ETag"},
		"guarded with lower-case if-match tag": {guarded(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *struct {
				IfMatch     string `header:"if-match"`
				IfNoneMatch string `header:"If-None-Match"`
			}) (*okOut, error) {
				return nil, nil
			})
		}, "If-Match"},
		"guarded DELETE with typed ETag": {guarded(http.MethodDelete), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*struct {
				ETag int `header:"ETag"`
			}, error) {
				return nil, nil
			})
		}, "must not declare"},
		"guarded with a default on If-Match": {guarded(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *struct {
				IfMatch     string `header:"If-Match" default:"*"`
				IfNoneMatch string `header:"If-None-Match"`
			}) (*okOut, error) {
				return nil, nil
			})
		}, "If-Match"},
		"guarded with framework-required If-Match": {guarded(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *struct {
				IfMatch     string `header:"If-Match" required:"true"`
				IfNoneMatch string `header:"If-None-Match"`
			}) (*okOut, error) {
				return nil, nil
			})
		}, "If-Match"},
		"conditional with a pattern on If-None-Match": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *struct {
				IfMatch     string `header:"If-Match"`
				IfNoneMatch string `header:"If-None-Match" pattern:"^\"[^\"]*\"$"`
			}) (*okOut, error) {
				return nil, nil
			})
		}, "If-None-Match"},
		"guarded with unexported If-Match": {guarded(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *unexportedIn) (*okOut, error) { return nil, nil })
		}, "If-Match"},
		"conditional with unexported Status": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*unexportedStatus, error) { return nil, nil })
		}, "Status"},
		"conditional with unexported ETag": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*unexportedOut, error) { return nil, nil })
		}, "ETag"},
		"guarded DELETE with ETag": {guarded(http.MethodDelete), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*okOut, error) { return nil, nil })
		}, "must not declare"},
		"guarded DELETE with Body": {guarded(http.MethodDelete), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*struct{ Body probeEcho }, error) { return nil, nil })
		}, "bodyless"},
		"guarded DELETE with Status": {guarded(http.MethodDelete), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*struct{ Status int }, error) { return nil, nil })
		}, "must not declare Status"},
		"conditional with embedded ETag": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*embeddedConditionalDocOutput, error) { return nil, nil })
		}, "ETag"},
		"create-only without If-Match": {createOnly(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *struct {
				IfNoneMatch string `header:"If-None-Match"`
			}) (*okOut, error) {
				return nil, nil
			})
		}, "If-Match"},
		"conditional without If-Match": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *struct {
				IfNoneMatch string `header:"If-None-Match"`
			}) (*okOut, error) {
				return nil, nil
			})
		}, "If-Match"},
		"create-only without If-None-Match": {createOnly(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *noHeaders) (*okOut, error) { return nil, nil })
		}, "If-None-Match"},
		"create-only without ETag": {createOnly(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noETag, error) { return nil, nil })
		}, "ETag"},
		"guarded without If-Match": {guarded(http.MethodPut), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *noHeaders) (*okOut, error) { return nil, nil })
		}, "If-Match"},
		"guarded without ETag": {guarded(http.MethodPatch), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noETag, error) { return nil, nil })
		}, "ETag"},
		"guarded with a non-string ETag": {guarded(http.MethodPatch), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*intETag, error) { return nil, nil })
		}, "ETag"},
		"conditional without If-None-Match": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *noHeaders) (*okOut, error) { return nil, nil })
		}, "If-None-Match"},
		"conditional without ETag": {conditional(http.MethodGet), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noETag, error) { return nil, nil })
		}, "ETag"},
		"conditional without Status": {conditional(http.MethodHead), func(r *Registry, op Operation) {
			Register(r, op, func(context.Context, *okIn) (*noStatus, error) { return nil, nil })
		}, "Status"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected a panic")
				}
				if !strings.Contains(fmt.Sprint(r), c.want) {
					t.Fatalf("panic %v does not mention %q", r, c.want)
				}
			}()
			newChiRouter(Dependencies{testRegister: func(reg *Registry) { c.reg(reg, c.op) }})
		})
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, guarded(method), func(context.Context, *okIn) (*okOut, error) { return nil, nil })
		}})
	}
	// A guarded DELETE answers 204 with no validator, so its output declares
	// none.
	newChiRouter(Dependencies{testRegister: func(reg *Registry) {
		Register(reg, guarded(http.MethodDelete), func(context.Context, *okIn) (*noHeaders, error) { return nil, nil })
	}})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, conditional(method), func(context.Context, *okIn) (*okOut, error) { return nil, nil })
		}})
	}
}
