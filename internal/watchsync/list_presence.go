package watchsync

import (
	"maps"
	"slices"
)

// remoteListPresence records the keys and IDs a complete provider read listed,
// each with the kinds of title listed under it and the key of the row that
// listed it. TMDB and TVDB number movies and series separately, so a movie row
// does not keep a series with the same key.
type remoteListPresence map[string]map[string]string

// add records a listed row: its key and the prefixed IDs it carries, all
// pointing at the key.
func (p remoteListPresence) add(kind, key string, ids ...string) {
	for _, id := range append([]string{key}, ids...) {
		if id == "" {
			continue
		}
		if p[id] == nil {
			p[id] = make(map[string]string)
		}
		p[id][kind] = key
	}
}

// listedKey returns the key of a listed row that carried id for a title of
// this kind. A row or stored state without a kind matches any kind.
func (p remoteListPresence) listedKey(kind, id string) (string, bool) {
	kinds := p[id]
	if key, ok := kinds[kind]; ok {
		return key, true
	}
	if key, ok := kinds[""]; ok {
		return key, true
	}
	if kind == "" && len(kinds) > 0 {
		return kinds[slices.Min(slices.Collect(maps.Keys(kinds)))], true
	}
	return "", false
}

// listed reports whether the read listed a title of this kind under key.
func (p remoteListPresence) listed(kind, key string) bool {
	_, ok := p.listedKey(kind, key)
	return ok
}

// listedKeyForAny returns the key the read listed a title of this kind under,
// found through the first of the prefixed IDs it carried.
func (p remoteListPresence) listedKeyForAny(kind string, ids []string) (string, bool) {
	for _, id := range ids {
		if key, ok := p.listedKey(kind, id); ok {
			return key, true
		}
	}
	return "", false
}
