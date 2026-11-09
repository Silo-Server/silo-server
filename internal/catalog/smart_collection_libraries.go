package catalog

import (
	"encoding/json"
	"slices"
	"strings"
)

// PinSmartRulesToCollectionLibraries rewrites a server smart collection's
// rules from before rules kept their own libraries, so they keep the titles
// they matched then. Reads used to narrow the rules to the collection's
// libraries, and rules naming no library took them. The result names those
// libraries: the collection's libraries when the rules named none, otherwise
// the ones the rules named that the collection is in.
//
// Rules naming none of the collection's libraries matched nothing, and an
// empty list now means every library, so they are returned unchanged, as are
// rules that are not a JSON object. Legacy section-filter rules
// (filter_type, filter_library_id, filter_library_ids) are read through
// filter_library_ids, so that key gets the list too.
//
// The migration that introduced the rule does the same to stored rows; a
// restore of an older backup applies it to the collections it creates.
func PinSmartRulesToCollectionLibraries(raw json.RawMessage, collectionLibraries []int) json.RawMessage {
	if len(collectionLibraries) == 0 {
		return raw
	}
	doc := map[string]json.RawMessage{}
	if text := strings.TrimSpace(string(raw)); text != "" && text != jsonNullText {
		if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
			return raw
		}
	}

	_, legacyType := doc["filter_type"]
	_, legacyOne := doc["filter_library_id"]
	_, legacyMany := doc["filter_library_ids"]
	legacy := legacyType || legacyOne || legacyMany

	var named []int
	if legacy {
		named = append(named, libraryIDsIn(doc["filter_library_ids"])...)
		var one int
		if json.Unmarshal(doc["filter_library_id"], &one) == nil && one > 0 {
			named = append(named, one)
		}
	} else {
		named = libraryIDsIn(doc["library_ids"])
	}

	var pinned []int
	for _, id := range collectionLibraries {
		if id > 0 && (len(named) == 0 || slices.Contains(named, id)) {
			pinned = append(pinned, id)
		}
	}
	slices.Sort(pinned)
	pinned = slices.Compact(pinned)
	if len(pinned) == 0 {
		return raw
	}

	encoded, _ := json.Marshal(pinned)
	doc["library_ids"] = encoded
	if legacy {
		doc["filter_library_ids"] = encoded
		delete(doc, "filter_library_id")
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// libraryIDsIn reads a JSON list of library ids, keeping the positive whole
// numbers; anything else reads as none.
func libraryIDsIn(raw json.RawMessage) []int {
	var values []json.Number
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil {
		return nil
	}
	ids := make([]int, 0, len(values))
	for _, value := range values {
		if id, err := value.Int64(); err == nil && id > 0 && id <= 1<<31-1 {
			ids = append(ids, int(id))
		}
	}
	return ids
}
