package settingscontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
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
	loadOnce      sync.Once
	loaded        *Manifest
	loadedErr     error
	loadedRaw     []byte
	loadedSchemas map[string][]byte
	objSchemas    map[string]*jsonschema.Schema
)

// loaded contract, as returned by load(). Keeping this a value rather than
// having load() assign the package globals means load() stays pure and can be
// called with a test filesystem without clobbering the process-wide contract.
type contract struct {
	manifest  *Manifest
	raw       []byte
	schemaRaw map[string][]byte
	compiled  map[string]*jsonschema.Schema
}

// Load returns the embedded canonical manifest, parsed and fully validated.
//
// It is loaded once per process. A malformed or self-inconsistent manifest is a
// build-time defect, not a runtime condition: the contract tests fail on it, and
// callers that reach this at runtime should treat the error as fatal at startup
// rather than degrading.
func Load() (*Manifest, error) {
	loadOnce.Do(func() {
		result, err := load(contractFS)
		if err != nil {
			loadedErr = err
			return
		}
		loaded = result.manifest
		loadedRaw = result.raw
		loadedSchemas = result.schemaRaw
		objSchemas = result.compiled
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

// SchemaBytes returns every value schema file exactly as checked in, keyed by
// file name. These decide which object-typed values the contract accepts, so
// they are part of its identity — see ETag.
func SchemaBytes() (map[string][]byte, error) {
	if _, err := Load(); err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(loadedSchemas))
	for name, body := range loadedSchemas {
		out[name] = append([]byte(nil), body...)
	}
	return out, nil
}

// ObjectSchema returns the compiled JSON Schema for an object-typed value.
func ObjectSchema(ref string) (*jsonschema.Schema, bool) {
	if _, err := Load(); err != nil {
		return nil, false
	}
	schema, ok := objSchemas[ref]
	return schema, ok
}

// ObjectSchemas returns every compiled object schema, keyed by schema_ref.
//
// ValidateValue needs the whole set rather than one entry, and it is called
// from outside this package now — the migration planner and the mutation
// endpoint both validate values they did not author. Returning the live map
// rather than a copy matches ValidateValue's own signature; callers treat it as
// read-only, and it is built once at load.
func ObjectSchemas() map[string]*jsonschema.Schema {
	if _, err := Load(); err != nil {
		return nil
	}
	return objSchemas
}

func load(fsys fs.FS) (contract, error) {
	raw, err := fs.ReadFile(fsys, manifestPath)
	if err != nil {
		return contract{}, fmt.Errorf("reading manifest: %w", err)
	}

	if err := validateAgainstManifestSchema(fsys, raw); err != nil {
		return contract{}, err
	}

	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return contract{}, fmt.Errorf("parsing manifest: %w", err)
	}

	if err := manifest.index(); err != nil {
		return contract{}, err
	}

	schemaRaw, compiled, err := compileObjectSchemas(fsys)
	if err != nil {
		return contract{}, err
	}

	if err := manifest.Validate(compiled); err != nil {
		return contract{}, err
	}

	return contract{manifest: &manifest, raw: raw, schemaRaw: schemaRaw, compiled: compiled}, nil
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
func compileObjectSchemas(fsys fs.FS) (map[string][]byte, map[string]*jsonschema.Schema, error) {
	entries, err := fs.ReadDir(fsys, schemasDir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading value schema directory: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	raw := make(map[string][]byte, len(entries))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// schema_ref can only name a .json file, so anything else here is not a
		// value schema. Parsing it anyway would turn a stray file — an editor
		// backup, a .DS_Store — into a panic at startup via MustLoad.
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		body, err := fs.ReadFile(fsys, path.Join(schemasDir, name))
		if err != nil {
			return nil, nil, fmt.Errorf("reading value schema %s: %w", name, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
		if err != nil {
			return nil, nil, fmt.Errorf("parsing value schema %s: %w", name, err)
		}
		if err := compiler.AddResource(name, doc); err != nil {
			return nil, nil, fmt.Errorf("registering value schema %s: %w", name, err)
		}
		raw[name] = body
		names = append(names, name)
	}

	compiled := make(map[string]*jsonschema.Schema, len(names))
	for _, name := range names {
		schema, err := compiler.Compile(name)
		if err != nil {
			return nil, nil, fmt.Errorf("compiling value schema %s: %w", name, err)
		}
		compiled[name] = schema
	}
	return raw, compiled, nil
}
