// Package userdata holds what internal/backup shares with the packages that
// own per-profile data. It imports nothing from them or from
// internal/backup, so any of them can use it without an import cycle.
package userdata

import "encoding/json"

// RemapQueryLibraries rewrites the library ids a query or source definition
// names (library_ids, filter_library_ids, filter_library_id) through
// mapLibrary. An id with no library here is dropped from its list, which
// only narrows the definition. A definition that would lose its whole scope
// is refused (false) rather than widened: a list that names libraries but
// none here would read as "every library", and so would a missing
// filter_library_id.
func RemapQueryLibraries(raw json.RawMessage, mapLibrary func(int) (int, bool)) (json.RawMessage, bool) {
	var doc map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return raw, true
	}
	remap := func(v any) (int, bool) {
		n, ok := v.(float64)
		if !ok {
			return 0, false
		}
		return mapLibrary(int(n))
	}
	changed := false
	for _, key := range []string{"library_ids", "filter_library_ids"} {
		list, ok := doc[key].([]any)
		if !ok {
			continue
		}
		out := []any{}
		for _, v := range list {
			if local, ok := remap(v); ok {
				out = append(out, local)
			}
		}
		if len(list) > 0 && len(out) == 0 {
			return nil, false
		}
		doc[key], changed = out, true
	}
	if v, ok := doc["filter_library_id"]; ok && v != nil {
		local, ok := remap(v)
		if !ok {
			return nil, false
		}
		doc["filter_library_id"], changed = local, true
	}
	if !changed {
		return raw, true
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	return data, true
}
