package contractspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"
)

// fixturesSchemaID is the $id the index schema declares; validation errors
// name it rather than a working-tree path.
const fixturesSchemaID = "https://siloserver.org/contracts/api/v2/fixtures.schema.json"

// openAPIResourceID is the synthetic URL the OpenAPI document is registered
// under so its component schemas can be compiled by JSON pointer.
const openAPIResourceID = "https://siloserver.org/contracts/api/v2/openapi.json"

const (
	mediaJSON    = "application/json"
	mediaProblem = "application/problem+json"
)

// FixtureIndex is the decoded contracts/api/v2/fixtures/index.json.
type FixtureIndex struct {
	Fixtures []Fixture `json:"fixtures"`
}

// Fixture is one index entry.
type Fixture struct {
	Name            string            `json:"name"`
	OperationID     *string           `json:"operation_id"`
	Scenario        string            `json:"scenario"`
	Request         FixtureRequest    `json:"request"`
	ExpectedStatus  int               `json:"expected_status"`
	ResponseHeaders map[string]string `json:"response_headers"`
	// The three are null on a bodyless status (204 or 304), which carries
	// no representation.
	ResponseMediaType *string `json:"response_media_type"`
	Schema            *string `json:"schema"`
	BodyFile          *string `json:"body_file"`
}

// FixtureRequest is the request an entry was generated from.
type FixtureRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// ValidateFixtures checks the fixture directory in fsys against the OpenAPI
// document: the index satisfies its schema, every body file the index names
// exists and satisfies the component schema the entry points at, every
// committed body is indexed, each entry's operation (when named) exists in
// the document with the entry's status documented, and each problem body's
// status equals expected_status. It returns every finding rather than the
// first.
func ValidateFixtures(fsys fs.FS, doc []byte) []string {
	var findings []string
	fail := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

	schemaBytes, err := fs.ReadFile(fsys, contracts.FixturesSchemaPath)
	if err != nil {
		return []string{err.Error()}
	}
	raw, err := fs.ReadFile(fsys, path.Join(contracts.FixturesDir, "index.json"))
	if err != nil {
		return []string{err.Error()}
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := addResource(compiler, fixturesSchemaID, schemaBytes); err != nil {
		return []string{err.Error()}
	}
	if err := addResource(compiler, openAPIResourceID, doc); err != nil {
		return []string{err.Error()}
	}
	indexSchema, err := compiler.Compile(fixturesSchemaID)
	if err != nil {
		return []string{err.Error()}
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return []string{"fixtures/index.json: " + err.Error()}
	}
	if err := indexSchema.Validate(instance); err != nil {
		return []string{"fixtures/index.json violates fixtures.schema.json: " + err.Error()}
	}
	var index FixtureIndex
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&index); err != nil {
		return []string{"fixtures/index.json: " + err.Error()}
	}

	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string                     `json:"operationId"`
			Responses   map[string]json.RawMessage `json:"responses"`
			Guarded     bool                       `json:"x-silo-guarded"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		return []string{err.Error()}
	}
	type opInfo struct {
		statuses map[string]bool
		guarded  bool
	}
	ops := map[string]opInfo{}
	for _, methods := range spec.Paths {
		for _, op := range methods {
			statuses := map[string]bool{}
			for s := range op.Responses {
				statuses[s] = true
			}
			ops[op.OperationID] = opInfo{statuses: statuses, guarded: op.Guarded}
		}
	}

	indexed := map[string]bool{"index.json": true}
	names := map[string]bool{}
	for _, f := range index.Fixtures {
		where := "fixtures/" + f.Name
		if names[f.Name] {
			fail("%s: duplicate fixture name", where)
		}
		names[f.Name] = true
		if f.ExpectedStatus == 204 || f.ExpectedStatus == 304 {
			if f.BodyFile != nil || f.Schema != nil || f.ResponseMediaType != nil {
				fail("%s: a %d fixture has no representation: body_file, schema and response_media_type must be null", where, f.ExpectedStatus)
			}
			if f.ExpectedStatus == 304 && f.ResponseHeaders["ETag"] == "" {
				fail("%s: a 304 fixture must record ETag", where)
			}
			// A guarded DELETE's 204 has no validator (Register refuses an
			// ETag on its output); an ordinary DELETE may carry one, as may
			// a bodyless 204 from a PUT or PATCH. Probe fixtures name no
			// operation, so they fall back to the method-only reading the
			// probes were written against.
			guarded := f.OperationID == nil || ops[*f.OperationID].guarded
			if f.ExpectedStatus == 204 && f.Request.Method == "DELETE" && guarded && f.ResponseHeaders["ETag"] != "" {
				fail("%s: a guarded 204 DELETE fixture records an ETag, but a deleted representation has no validator", where)
			}
			if f.OperationID != nil {
				if info, ok := ops[*f.OperationID]; !ok || !info.statuses[fmt.Sprint(f.ExpectedStatus)] {
					fail("%s: openapi.json does not document status %d on %s", where, f.ExpectedStatus, *f.OperationID)
				}
			} else if !strings.HasPrefix(f.Request.Path, "/api/v2/probe/") {
				fail("%s: a bodyless fixture without an operation must target a probe path", where)
			}
			continue
		}
		if f.BodyFile == nil || f.Schema == nil || f.ResponseMediaType == nil {
			fail("%s: body_file, schema and response_media_type are required outside a bodyless 204 or 304", where)
			continue
		}
		bodyFile, schemaRef, mediaType := *f.BodyFile, *f.Schema, *f.ResponseMediaType
		if bodyFile != f.Name+".json" {
			fail("%s: body_file %q must be %s.json", where, bodyFile, f.Name)
		}
		indexed[bodyFile] = true
		if f.OperationID != nil {
			info, ok := ops[*f.OperationID]
			switch {
			case !ok:
				fail("%s: operation %q is not in openapi.json", where, *f.OperationID)
			case !info.statuses[fmt.Sprint(f.ExpectedStatus)]:
				fail("%s: openapi.json does not document status %d on %s", where, f.ExpectedStatus, *f.OperationID)
			}
		} else if !strings.HasPrefix(f.Request.Path, "/api/v2/probe/") && f.ExpectedStatus != 404 {
			fail("%s: a fixture without an operation must target a probe path or be the 404 fallback", where)
		}
		wantMedia := mediaJSON
		if f.ExpectedStatus >= 400 {
			wantMedia = mediaProblem
		}
		if mediaType != wantMedia {
			fail("%s: response_media_type %q, want %q for status %d", where, mediaType, wantMedia, f.ExpectedStatus)
		}
		if ct := f.ResponseHeaders["Content-Type"]; !strings.HasPrefix(ct, wantMedia) {
			fail("%s: response_headers.Content-Type %q does not match the media type", where, ct)
		}
		if f.ExpectedStatus == 429 && f.ResponseHeaders["Retry-After"] == "" {
			fail("%s: a 429 fixture must record Retry-After", where)
		}
		body, err := fs.ReadFile(fsys, path.Join(contracts.FixturesDir, bodyFile))
		if err != nil {
			fail("%s: %v", where, err)
			continue
		}
		schema, err := compiler.Compile(openAPIResourceID + schemaRef)
		if err != nil {
			fail("%s: schema %s: %v", where, schemaRef, err)
			continue
		}
		bodyInstance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
		if err != nil {
			fail("%s: body is not JSON: %v", where, err)
			continue
		}
		if err := schema.Validate(bodyInstance); err != nil {
			fail("%s: body violates %s: %v", where, schemaRef, err)
		}
		if wantMedia == mediaProblem {
			var p struct {
				Status   int    `json:"status"`
				Instance string `json:"instance"`
			}
			if err := json.Unmarshal(body, &p); err == nil {
				if p.Status != f.ExpectedStatus {
					fail("%s: problem status %d != expected_status %d", where, p.Status, f.ExpectedStatus)
				}
				if !strings.HasPrefix(p.Instance, "urn:silo:request:") {
					fail("%s: problem instance %q is not a request URN", where, p.Instance)
				}
			}
		}
	}
	entries, err := fs.ReadDir(fsys, contracts.FixturesDir)
	if err != nil {
		return append(findings, err.Error())
	}
	for _, e := range entries {
		if !indexed[e.Name()] {
			fail("fixtures/%s is committed but not indexed", e.Name())
		}
	}
	sort.Strings(findings)
	return findings
}

func addResource(c *jsonschema.Compiler, id string, raw []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	return c.AddResource(id, v)
}
