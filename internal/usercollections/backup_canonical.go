package usercollections

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

// An imported collection's sort and sync source are decoded into their
// types and stored as the editors encode them, never as the document's
// bytes: a field either type lacks is refused, and so is a list URL its
// editor would not store.

// maxSourceText bounds each text field of an imported sync source, as the
// collection's source URL is bounded.
const maxSourceText = 2048

// maxSourceLibraries bounds the libraries an imported sync source names.
const maxSourceLibraries = 1000

var errDefinition = errors.New("not one this server accepts")

// decodeStrictly decodes raw into into, refusing members into lacks and
// anything after the value.
func decodeStrictly(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.More() {
		return errDefinition
	}
	return nil
}

// emptyJSON reports a missing, null, or empty-object value.
func emptyJSON(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null" || trimmed == "{}"
}

// canonicalSort is a sort_config as the collection editor stores one: {}
// for the collection's own order (the legacy mode members, which select
// none, included), else its field and order (catalog.NormalizeCollectionSort).
func canonicalSort(raw json.RawMessage) (json.RawMessage, error) {
	if emptyJSON(raw) {
		return json.RawMessage("{}"), nil
	}
	var cfg struct {
		Field string `json:"field"`
		Order string `json:"order"`
		Mode  string `json:"mode"`
	}
	if err := decodeStrictly(raw, &cfg); err != nil {
		return nil, fmt.Errorf("its sort is %w", errDefinition)
	}
	if strings.TrimSpace(cfg.Field) == "" {
		return json.RawMessage("{}"), nil
	}
	qs, ok := catalog.NormalizeCollectionSort(cfg.Field, cfg.Order, true)
	encoded, err := catalog.EncodeCollectionDefaultSort(qs, ok)
	if err != nil || !ok {
		return nil, fmt.Errorf("its sort is %w", errDefinition)
	}
	return json.RawMessage(encoded), nil
}

// The collection types that sync from a source.
const (
	collectionTypeMDBList = "mdblist"
	collectionTypeTMDB    = "tmdb"
)

// sourceModes are the sync sources each collection type has; every other
// type has none.
var sourceModes = map[string]map[SourceMode]bool{
	collectionTypeMDBList: {SourceModeMDBList: true},
	collectionTypeTMDB:    {SourceModeTMDBPreset: true, SourceModeTMDBList: true},
}

// canonicalSource is a collection's source URL and source_config as the
// editors store them: the configuration decoded into SourceConfig, its mode
// one the collection's type syncs from (none for a type that syncs from
// nothing), its text bounded, and its list URLs, with the source URL, in
// their editors' canonical form (collectionutil.CanonicalCollectionSource).
func canonicalSource(collectionType, sourceURL string, raw json.RawMessage) (string, json.RawMessage, error) {
	refused := func(what string) error { return fmt.Errorf("its sync source %s is %w", what, errDefinition) }
	var cfg SourceConfig
	if !emptyJSON(raw) {
		if err := decodeStrictly(raw, &cfg); err != nil {
			return "", nil, refused("configuration")
		}
	}
	if cfg.Mode != "" && !sourceModes[collectionType][cfg.Mode] {
		return "", nil, refused("mode")
	}
	for _, s := range []string{string(cfg.Mode), cfg.URL, cfg.Preset, cfg.Provider, cfg.MediaType, cfg.TimeWindow, cfg.ProfileID} {
		if len(s) > maxSourceText {
			return "", nil, refused("text")
		}
	}
	if len(cfg.LibraryIDs) > maxSourceLibraries {
		return "", nil, refused("library list")
	}
	if emptyJSON(raw) && cfg.Mode == "" {
		url, _, err := collectionutil.CanonicalCollectionSource(collectionType, sourceURL, nil)
		if err != nil {
			return "", nil, refused("URL")
		}
		return url, json.RawMessage("{}"), nil
	}
	encoded, _ := json.Marshal(cfg) // a struct of strings and ints
	url, canonical, err := collectionutil.CanonicalCollectionSource(collectionType, sourceURL, encoded)
	if err != nil {
		return "", nil, refused("URL")
	}
	return url, canonical, nil
}
