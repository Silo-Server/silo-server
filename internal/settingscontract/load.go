package settingscontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	settingsv1 "github.com/Silo-Server/silo-server/contracts/settings/v1"
)

// contractFS is the embedded canonical contract. Clients vendor a pinned copy
// of the same files.
var contractFS = settingsv1.FS

const (
	manifestPath       = "manifest.json"
	manifestSchemaPath = "manifest.schema.json"
	schemasDir         = "schemas"
)

var (
	loadOnce   sync.Once
	loaded     *Manifest
	loadedErr  error
	loadedRaw  []byte
	objSchemas map[string]*jsonschema.Schema
)

// Load returns the embedded canonical manifest, parsed and fully validated.
//
// It is loaded once per process. A malformed or self-inconsistent manifest is a
// build-time defect, not a runtime condition: the contract tests fail on it, and
// callers that reach this at runtime should treat the error as fatal at startup
// rather than degrading.
func Load() (*Manifest, error) {
	loadOnce.Do(func() {
		loaded, loadedRaw, loadedErr = load(contractFS)
	})
	return loaded, loadedErr
}

// MustLoad returns the embedded manifest or panics. For use in server startup
// where a broken embedded contract cannot be recovered from.
func MustLoad() *Manifest {
	m, err := Load()
	if err != nil {
		panic(fmt.Sprintf("settingscontract: embedded manifest is invalid: %v", err))
	}
	return m
}

// RawBytes returns the embedded manifest file exactly as checked in.
func RawBytes() ([]byte, error) {
	if _, err := Load(); err != nil {
		return nil, err
	}
	return append([]byte(nil), loadedRaw...), nil
}

// ObjectSchema returns the compiled JSON Schema for an object-typed value.
func ObjectSchema(ref string) (*jsonschema.Schema, bool) {
	if _, err := Load(); err != nil {
		return nil, false
	}
	schema, ok := objSchemas[ref]
	return schema, ok
}

func load(fsys fs.FS) (*Manifest, []byte, error) {
	raw, err := fs.ReadFile(fsys, manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading manifest: %w", err)
	}

	if err := validateAgainstManifestSchema(fsys, raw); err != nil {
		return nil, nil, err
	}

	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, nil, fmt.Errorf("parsing manifest: %w", err)
	}

	if err := manifest.index(); err != nil {
		return nil, nil, err
	}

	schemas, err := compileObjectSchemas(fsys)
	if err != nil {
		return nil, nil, err
	}
	objSchemas = schemas

	if err := manifest.Validate(schemas); err != nil {
		return nil, nil, err
	}

	return &manifest, raw, nil
}

// validateAgainstManifestSchema checks the manifest file against its own JSON
// Schema before Go decoding, so shape errors report as schema violations with a
// location rather than as opaque unmarshal failures.
func validateAgainstManifestSchema(fsys fs.FS, raw []byte) error {
	schemaBytes, err := fs.ReadFile(fsys, manifestSchemaPath)
	if err != nil {
		return fmt.Errorf("reading manifest schema: %w", err)
	}

	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return fmt.Errorf("parsing manifest schema: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(manifestSchemaPath, schemaDoc); err != nil {
		return fmt.Errorf("registering manifest schema: %w", err)
	}
	schema, err := compiler.Compile(manifestSchemaPath)
	if err != nil {
		return fmt.Errorf("compiling manifest schema: %w", err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("manifest does not satisfy manifest.schema.json: %w", err)
	}
	return nil
}

// compileObjectSchemas compiles every schema under schemas/ so object-typed
// values and their defaults can be validated. Compiling all of them up front
// also catches a malformed schema file that no definition happens to reference
// yet.
func compileObjectSchemas(fsys fs.FS) (map[string]*jsonschema.Schema, error) {
	entries, err := fs.ReadDir(fsys, schemasDir)
	if err != nil {
		return nil, fmt.Errorf("reading value schema directory: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		body, err := fs.ReadFile(fsys, path.Join(schemasDir, name))
		if err != nil {
			return nil, fmt.Errorf("reading value schema %s: %w", name, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("parsing value schema %s: %w", name, err)
		}
		if err := compiler.AddResource(name, doc); err != nil {
			return nil, fmt.Errorf("registering value schema %s: %w", name, err)
		}
		names = append(names, name)
	}

	compiled := make(map[string]*jsonschema.Schema, len(names))
	for _, name := range names {
		schema, err := compiler.Compile(name)
		if err != nil {
			return nil, fmt.Errorf("compiling value schema %s: %w", name, err)
		}
		compiled[name] = schema
	}
	return compiled, nil
}
