package collectionutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The source modes whose configuration names a list the sync fetches by
// URL.
const (
	sourceModeMDBList  = "mdblist_json"
	sourceModeTMDBList = "tmdb_list"
	collectionMDBList  = "mdblist"
)

// ErrCollectionSource refuses a collection source the editors would not
// store.
var ErrCollectionSource = errors.New("its source is not one this server accepts")

// CanonicalCollectionSource checks a collection's source (its type, source
// URL, and source configuration) the way the editors build one, for a
// collection a restore or import writes from a document: each list URL the
// sync fetches is run through its canonicalizer (CanonicalMDBListURL for an
// MDBList collection or mdblist_json source, CanonicalTMDBListURL for a
// tmdb_list source) and returned in canonical form, and anything else is
// refused. A configuration that is not a JSON object is refused too; an
// empty one stays empty.
func CanonicalCollectionSource(collectionType, sourceURL string, config json.RawMessage) (string, json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if trimmed := strings.TrimSpace(string(config)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(config, &fields); err != nil || fields == nil {
			return "", nil, fmt.Errorf("%w: its configuration is not an object", ErrCollectionSource)
		}
	}
	var mode string
	if raw, ok := fields["mode"]; ok {
		if err := json.Unmarshal(raw, &mode); err != nil {
			return "", nil, fmt.Errorf("%w: its mode is not a string", ErrCollectionSource)
		}
	}
	var canonical func(string) (string, error)
	switch {
	case mode == sourceModeTMDBList:
		canonical = CanonicalTMDBListURL
	case mode == sourceModeMDBList || strings.EqualFold(strings.TrimSpace(collectionType), collectionMDBList):
		canonical = CanonicalMDBListURL
	}
	for _, key := range []string{"url", "list_url"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", nil, fmt.Errorf("%w: its %s is not a string", ErrCollectionSource, key)
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		if canonical == nil {
			return "", nil, fmt.Errorf("%w: a %q source names no list URL", ErrCollectionSource, mode)
		}
		normalized, err := canonical(value)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %w", ErrCollectionSource, err)
		}
		fields[key], _ = json.Marshal(normalized) // a string
	}
	if canonical != nil && strings.TrimSpace(sourceURL) != "" {
		normalized, err := canonical(sourceURL)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %w", ErrCollectionSource, err)
		}
		sourceURL = normalized
	}
	if fields == nil {
		return sourceURL, config, nil
	}
	out, err := json.Marshal(fields)
	return sourceURL, out, err
}
