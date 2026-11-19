package bookid

import "strings"

// Work-level identifier keys. A value under one of them names the book an
// edition belongs to, not the edition itself, so every edition of a book can
// hold the same value. For audiobook and ebook items they are kept out of
// media_item_provider_ids, where a value names one item, and stored as
// claims in media_item_work_ids.
const (
	KeyOpenLibraryWork = "openlibrary_work"
	KeyHardcover       = "hardcover"
)

// Evidence values: how a plugin reached a work from the edition it returned.
const (
	EvidenceISBN        = "isbn"
	EvidenceTitleAuthor = "title_author"
	EvidenceUnknown     = "unknown"
)

// catalogWorkKeys maps the catalog a plugin names in work_lookup_failed, when
// it could not ask that catalog, to the work key the catalog supplies.
var catalogWorkKeys = map[string]string{
	"openlibrary": KeyOpenLibraryWork,
	"hardcover":   KeyHardcover,
}

// WorkKey returns provider folded to its stored form (trimmed, lower case)
// and whether that is a work-level key.
func WorkKey(provider string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(provider))
	return key, key == KeyOpenLibraryWork || key == KeyHardcover
}

// HasWorkIDs reports whether items of itemType keep work-level keys apart
// from their edition identifiers.
func HasWorkIDs(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "audiobook", "ebook":
		return true
	}
	return false
}

// WorkClaim is one work id a source returned for an item.
type WorkClaim struct {
	Provider   string
	ProviderID string
	Evidence   string
}

// WorkClaims is what one metadata answer said about an item's works. Source
// is the capability that answered. Claims holds the work ids it returned;
// LookupFailed the catalogs it could not ask, whose earlier claims from
// Source stand.
type WorkClaims struct {
	Source       string
	Claims       []WorkClaim
	LookupFailed []string
}

// KeptKeys returns the work keys whose claims from c.Source outlive an answer
// that left them out, because the catalog behind the key could not be asked.
func (c WorkClaims) KeptKeys() []string {
	var keys []string
	for _, catalog := range c.LookupFailed {
		if key, ok := catalogWorkKeys[catalog]; ok {
			keys = append(keys, key)
		}
	}
	return keys
}

// NormalizeEvidence returns evidence when it is a known value, else
// EvidenceUnknown.
func NormalizeEvidence(evidence string) string {
	switch evidence = strings.ToLower(strings.TrimSpace(evidence)); evidence {
	case EvidenceISBN, EvidenceTitleAuthor:
		return evidence
	}
	return EvidenceUnknown
}

// SplitWorkIDs separates ids into edition identifiers and work ids (keyed by
// their stored form, values trimmed) for an item of itemType. For other item
// types every key is an edition identifier and work is nil. ids is not
// modified.
func SplitWorkIDs(itemType string, ids map[string]string) (edition, work map[string]string) {
	if !HasWorkIDs(itemType) {
		return ids, nil
	}
	edition = make(map[string]string, len(ids))
	for provider, value := range ids {
		key, isWork := WorkKey(provider)
		if !isWork {
			edition[provider] = value
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			if work == nil {
				work = make(map[string]string)
			}
			work[key] = value
		}
	}
	return edition, work
}
