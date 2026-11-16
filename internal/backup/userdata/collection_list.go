package userdata

import "encoding/json"

// CollectionListField is the configuration field of a row of collections:
// an ordered array of objects, each naming one collection in
// LibraryCollectionField or UserCollectionField.
const CollectionListField = "collections"

// RemapCollectionList maps each entry of a configuration's collections list
// through mapID, which is given the entry's field and id and answers the
// local id, or false to drop the entry. Order is kept. It returns the
// configuration with the mapped list and how many entries the list had and
// kept. A configuration with no list comes back unchanged, with total 0.
func RemapCollectionList(config json.RawMessage, mapID func(field, id string) (string, bool, error)) (out json.RawMessage, total, kept int, err error) {
	doc, entries, listed := collectionList(config)
	if !listed {
		return config, 0, 0, nil
	}
	mapped := make([]map[string]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		for _, field := range []string{LibraryCollectionField, UserCollectionField} {
			var id string
			if json.Unmarshal(entry[field], &id) != nil || id == "" {
				continue
			}
			local, ok, err := mapID(field, id)
			if err != nil {
				return nil, 0, 0, err
			}
			if ok {
				entry[field], _ = json.Marshal(local) // a string
				mapped = append(mapped, entry)
			}
			break
		}
	}
	doc[CollectionListField], err = json.Marshal(mapped)
	if err != nil {
		return nil, 0, 0, err
	}
	out, err = json.Marshal(doc)
	if err != nil {
		return nil, 0, 0, err
	}
	return out, len(entries), len(mapped), nil
}

// collectionList decodes a configuration and its collections list; false
// when it is not an object or has no list.
func collectionList(config json.RawMessage) (map[string]json.RawMessage, []map[string]json.RawMessage, bool) {
	var doc map[string]json.RawMessage
	if len(config) == 0 || json.Unmarshal(config, &doc) != nil {
		return nil, nil, false
	}
	var entries []map[string]json.RawMessage
	raw, ok := doc[CollectionListField]
	if !ok || json.Unmarshal(raw, &entries) != nil {
		return nil, nil, false
	}
	return doc, entries, true
}
