package userstore

import (
	"errors"
	"slices"
)

// TitleAllowance is one movie or series a limited profile may watch above its
// maturity limits.
type TitleAllowance struct {
	ContentID string
	CreatedAt string
}

// CollectionAllowance is a personal collection whose titles a limited profile
// may watch above its maturity limits. Live reports whether the collection
// currently allows anything: only a manual collection created by the
// account's primary profile does (see pgstore.ProfileAllowedTitleIDsSQL).
type CollectionAllowance struct {
	CollectionID string
	Name         string
	ItemCount    int
	Live         bool
	CreatedAt    string
}

// MaxAllowedTitles bounds the allowed titles one profile's read takes,
// directly and through live collections together. The list is read with
// every request the profile makes, so a profile allowed more than this sees
// only this many of them, and the rest stay hidden, rather than slowing every
// request.
const MaxAllowedTitles = 10_000

// CollectionPosition is a stored collection entry's place in the
// collection's order, the key a copy pages by.
type CollectionPosition struct {
	Position    int
	MediaItemID string
}

// CollectionTypeManual is the collection_type of a hand-picked personal
// collection, the only kind a profile can be allowed as it changes.
const CollectionTypeManual = "manual"

// AllowanceTitleTypes are the media_items types an allowance may name. A
// series allowance covers its seasons and episodes, so neither is allowed on
// its own.
var AllowanceTitleTypes = []string{"movie", "series"}

// AllowableTitleType reports whether an item of this type can be allowed.
func AllowableTitleType(itemType string) bool {
	return slices.Contains(AllowanceTitleTypes, itemType)
}

// AllowanceManager is who writes an allowance: the account, and the acting
// primary profile with the PIN revision its authorization saw. A write locks
// that profile and refuses with ErrAllowanceManagerChanged when its PIN
// changed since, so revoked PIN proof cannot finish a pending write. An empty
// ProfileID (an account-level caller) skips the check.
type AllowanceManager struct {
	UserID      int
	ProfileID   string
	PINRevision int64
}

// ErrAllowanceManagerChanged reports that the acting profile's PIN changed
// after the write was authorized.
var ErrAllowanceManagerChanged = errors.New("allowance manager PIN changed")
