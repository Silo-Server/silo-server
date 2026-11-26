package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/appkeys"
)

// A profile set item's PIN instruction.
const (
	profileSetPINSet   = "set"
	profileSetPINKeep  = "keep"
	profileSetPINClear = "clear"
)

// Profile set limits.
const (
	maxProfileSetItems          = 64
	maxProfileSetName           = 64
	maxProfileSetPicture        = 200
	maxProfileSetRating         = 32
	maxProfileSetResetKeys      = 256
	profileSetPicturePrefixHash = "sha256:"
)

var (
	profileSetPINPattern  = regexp.MustCompile(`^[0-9]{4}$`)
	profileSetHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ProfileSetFieldError refuses a malformed profile set. Location is the
// member it names, relative to the request body (set.items[2].name,
// pairs[0].local_id); a signed set reports it at the signed payload instead.
type ProfileSetFieldError struct {
	Location string
	// Code is the validation code: invalid, required for a member left out,
	// or name_conflict for a name that two profiles would share: two items of
	// the set named alike, an item named like a profile outside the set, or
	// one named like a name the owner gave another of the set's profiles.
	// A set that names two items alike is well formed; the server alone
	// decides whether two names are the same, so the clash is judged with
	// the household, after the version, like any other.
	Code    string
	Message string
}

func (e *ProfileSetFieldError) Error() string { return e.Location + ": " + e.Message }

func profileSetFieldError(location, message string) *ProfileSetFieldError {
	return &ProfileSetFieldError{Location: location, Code: "invalid", Message: message}
}

// profileSetRequired is the error for a required member an item leaves out.
// An absent member is never read as false, empty or "no limit".
func profileSetRequired(at, name string) *ProfileSetFieldError {
	return &ProfileSetFieldError{Location: at + "." + name, Code: "required", Message: "expected required property " + name + " to be present"}
}

// profileSetPIN is an item's PIN instruction: its kind, and the four digits
// of a set.
type profileSetPIN struct {
	kind   string
	digits string
}

// profileSetItem is one parsed item. Members the server does not know are
// ignored, but they stay in raw, which the content hash covers.
type profileSetItem struct {
	SyncID             string
	Name               string
	Picture            string
	PIN                profileSetPIN
	IsChild            bool
	MaxContentRating   string
	MaxAdvisoryAge     int
	RequireAdvisoryAge bool
	RequestsBlocked    bool
	Settings           map[string]json.RawMessage
	SettingsReset      []string
	raw                map[string]json.RawMessage
}

type profileSetPair struct {
	SyncID  string
	LocalID string
}

// profileSetDoc is a parsed profile set with its content hash.
type profileSetDoc struct {
	Version   int64
	Items     []profileSetItem
	Pairs     []profileSetPair
	EditsSeen string
	Hash      []byte
}

// syncIDs lists the set's sync IDs in order.
func (d *profileSetDoc) syncIDs() []string {
	out := make([]string, len(d.Items))
	for i, item := range d.Items {
		out[i] = item.SyncID
	}
	return out
}

// parseProfileSet reads a set and its optional pairs, the same for the
// signed and the session form. It refuses a malformed member and ignores an
// item member it does not know.
// CheckProfileSet reports whether a set, its pairs and its edits_seen are
// well formed, with the *ProfileSetFieldError that applying them would
// answer. The route runs it for both forms of the request before it spends
// or locks anything.
func CheckProfileSet(set, pairs json.RawMessage, editsSeen string) error {
	_, err := parseProfileSet(set, pairs, editsSeen)
	return err
}

func parseProfileSet(set, pairs json.RawMessage, editsSeen string) (*profileSetDoc, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(set, &top); err != nil || top == nil {
		return nil, profileSetFieldError("set", "The set must be an object.")
	}
	version, err := appkeys.WholeNumber(top["version"])
	if err != nil {
		return nil, profileSetFieldError("set.version", "The version must be a whole number from 0 to 2^53 - 1.")
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(top["items"], &rawItems); err != nil || len(rawItems) == 0 || len(rawItems) > maxProfileSetItems {
		return nil, profileSetFieldError("set.items", fmt.Sprintf("The set must hold 1 to %d items.", maxProfileSetItems))
	}
	if utf8.RuneCountInString(editsSeen) > appkeys.MaxEditsSeenLength || hasControlCharacter(editsSeen) {
		return nil, profileSetFieldError("edits_seen", "edits_seen is at most 64 characters, with no control characters.")
	}
	doc := &profileSetDoc{Version: version, EditsSeen: editsSeen}
	syncIDs := map[string]bool{}
	for i, raw := range rawItems {
		item, err := parseProfileSetItem(raw, fmt.Sprintf("set.items[%d]", i))
		if err != nil {
			return nil, err
		}
		if syncIDs[item.SyncID] {
			return nil, profileSetFieldError(fmt.Sprintf("set.items[%d].sync_id", i), "Two items have this sync_id.")
		}
		syncIDs[item.SyncID] = true
		doc.Items = append(doc.Items, item)
	}
	if doc.Pairs, err = parseProfileSetPairs(pairs, syncIDs); err != nil {
		return nil, err
	}
	if doc.Hash, err = profileSetContentHash(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func parseProfileSetItem(raw json.RawMessage, at string) (profileSetItem, error) {
	var item profileSetItem
	if err := json.Unmarshal(raw, &item.raw); err != nil || item.raw == nil {
		return item, profileSetFieldError(at, "An item must be an object.")
	}
	field := func(name string, target any, message string) error {
		value, ok := item.raw[name]
		if !ok {
			return profileSetRequired(at, name)
		}
		if !decodeStrict(value, target) {
			return profileSetFieldError(at+"."+name, message)
		}
		return nil
	}
	if err := field("sync_id", &item.SyncID, "sync_id must be a lower-case UUID."); err != nil {
		return item, err
	}
	if parsed, err := uuid.Parse(item.SyncID); err != nil || parsed.String() != item.SyncID {
		return item, profileSetFieldError(at+".sync_id", "sync_id must be a lower-case UUID.")
	}
	if err := field("name", &item.Name, "name must be a string."); err != nil {
		return item, err
	}
	item.Name = strings.TrimSpace(item.Name)
	if n := utf8.RuneCountInString(item.Name); n == 0 || n > maxProfileSetName || hasControlCharacter(item.Name) {
		return item, profileSetFieldError(at+".name", fmt.Sprintf("name must be 1 to %d characters, with no control characters.", maxProfileSetName))
	}
	if err := field("picture", &item.Picture, "picture must be a string."); err != nil {
		return item, err
	}
	if !validProfileSetPicture(item.Picture) {
		return item, profileSetFieldError(at+".picture", "picture must be empty, preset:<id>, or sha256:<64 lower-case hex>.")
	}
	rawPIN, ok := item.raw["pin"]
	if !ok {
		return item, profileSetRequired(at, "pin")
	}
	pin, err := parseProfileSetPIN(rawPIN, at+".pin")
	if err != nil {
		return item, err
	}
	item.PIN = pin
	for name, target := range map[string]*bool{
		"is_child": &item.IsChild, "require_advisory_age": &item.RequireAdvisoryAge, "requests_blocked": &item.RequestsBlocked,
	} {
		if err := field(name, target, name+" must be a boolean."); err != nil {
			return item, err
		}
	}
	if err := field("max_content_rating", &item.MaxContentRating, "max_content_rating must be a string."); err != nil {
		return item, err
	}
	if utf8.RuneCountInString(item.MaxContentRating) > maxProfileSetRating || hasControlCharacter(item.MaxContentRating) {
		return item, profileSetFieldError(at+".max_content_rating", "max_content_rating is at most 32 characters.")
	}
	var age *int
	if err := field("max_advisory_age", &age, "max_advisory_age must be null or a whole number."); err != nil {
		return item, err
	}
	if age != nil {
		if !access.ValidAdvisoryAgeLimit(*age) {
			return item, profileSetFieldError(at+".max_advisory_age", fmt.Sprintf(
				"max_advisory_age must be null or from %d to %d.", access.MinAdvisoryAgeLimit, access.MaxAdvisoryAgeLimit))
		}
		item.MaxAdvisoryAge = *age
	}
	if err := parseProfileSetSettings(&item, at); err != nil {
		return item, err
	}
	return item, nil
}

func parseProfileSetPIN(raw json.RawMessage, at string) (profileSetPIN, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return profileSetPIN{}, profileSetFieldError(at, `pin must be {"set": "<4 digits>"}, {"keep": true} or {"clear": true}.`)
	}
	var out profileSetPIN
	count := 0
	for _, kind := range []string{profileSetPINSet, profileSetPINKeep, profileSetPINClear} {
		value, ok := members[kind]
		if !ok {
			continue
		}
		count++
		out.kind = kind
		if kind == profileSetPINSet {
			if !decodeStrict(value, &out.digits) || !profileSetPINPattern.MatchString(out.digits) {
				return profileSetPIN{}, profileSetFieldError(at+".set", "A PIN sent as set is exactly four digits.")
			}
			continue
		}
		var flag bool
		if !decodeStrict(value, &flag) || !flag {
			return profileSetPIN{}, profileSetFieldError(at+"."+kind, kind+" must be true.")
		}
	}
	if count != 1 {
		return profileSetPIN{}, profileSetFieldError(at, `pin must be exactly one of set, keep or clear.`)
	}
	return out, nil
}

// parseProfileSetSettings reads an item's settings. The member is required,
// and {} is how an app says it has no setting to apply: a key in settings is
// set, a key in settings_reset is reset, and a key in neither is left as the
// server has it. An absent member would mean the same as {}, which is why it
// is refused: it would let an app that forgot the member pass for one that
// chose to send nothing. settings_reset is optional and defaults to empty.
func parseProfileSetSettings(item *profileSetItem, at string) error {
	raw, ok := item.raw["settings"]
	if !ok {
		return profileSetRequired(at, "settings")
	}
	if !decodeStrict(raw, &item.Settings) || item.Settings == nil {
		return profileSetFieldError(at+".settings", "settings must be an object.")
	}
	if raw, ok := item.raw["settings_reset"]; ok {
		if !decodeStrict(raw, &item.SettingsReset) || item.SettingsReset == nil || len(item.SettingsReset) > maxProfileSetResetKeys {
			return profileSetFieldError(at+".settings_reset", "settings_reset must be an array of at most 256 keys.")
		}
	}
	for i, key := range item.SettingsReset {
		if _, both := item.Settings[key]; both {
			return profileSetFieldError(fmt.Sprintf("%s.settings_reset[%d]", at, i), "A key cannot be both set and reset.")
		}
	}
	slices.Sort(item.SettingsReset)
	item.SettingsReset = slices.Compact(item.SettingsReset)
	return nil
}

func parseProfileSetPairs(raw json.RawMessage, syncIDs map[string]bool) ([]profileSetPair, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, profileSetFieldError("pairs", "pairs must be an array of {sync_id, local_id}.")
	}
	seenSync, seenLocal := map[string]bool{}, map[string]bool{}
	out := make([]profileSetPair, 0, len(items))
	for i, members := range items {
		at := fmt.Sprintf("pairs[%d]", i)
		var pair profileSetPair
		if !decodeStrict(members["sync_id"], &pair.SyncID) || !syncIDs[pair.SyncID] {
			return nil, profileSetFieldError(at+".sync_id", "sync_id must name an item of the set.")
		}
		if !decodeStrict(members["local_id"], &pair.LocalID) || pair.LocalID == "" {
			return nil, profileSetFieldError(at+".local_id", "local_id must name a profile on this server.")
		}
		if seenSync[pair.SyncID] {
			return nil, profileSetFieldError(at+".sync_id", "Two pairs name this sync_id.")
		}
		if seenLocal[pair.LocalID] {
			return nil, profileSetFieldError(at+".local_id", "Two pairs name this profile.")
		}
		seenSync[pair.SyncID], seenLocal[pair.LocalID] = true, true
		out = append(out, pair)
	}
	return out, nil
}

func validProfileSetPicture(picture string) bool {
	switch {
	case picture == "":
		return true
	case strings.HasPrefix(picture, profileAvatarPresetPrefix):
		id := strings.TrimPrefix(picture, profileAvatarPresetPrefix)
		return id != "" && len(picture) <= maxProfileSetPicture && !hasControlCharacter(id)
	case strings.HasPrefix(picture, profileSetPicturePrefixHash):
		return profileSetHashPattern.MatchString(strings.TrimPrefix(picture, profileSetPicturePrefixHash))
	}
	return false
}

// decodeStrict decodes a present JSON value into target. JSON null decodes
// only into a pointer, so a null never passes as a zero string or false.
func decodeStrict(raw json.RawMessage, target any) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	if bytes.Equal(raw, []byte("null")) {
		_, isPointer := target.(**int)
		return isPointer
	}
	return json.Unmarshal(raw, target) == nil
}

func hasControlCharacter(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// sameProfileName reports whether two profile names are the same household
// member's: trimmed and compared without case, the rule
// profileNameConflicts applies.
func sameProfileName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// profileSetContentHash names a set's content without its PIN digits: the
// SHA-256 of a canonical JSON document holding the version and each item as
// sent, members the server does not know included, with keys sorted, numbers
// read as doubles, and pin reduced to its kind. Two sets that differ only in
// a PIN's digits get the same hash; a plain hash of a one-profile set would
// give a four-digit PIN up faster than its bcrypt hash would. pairs and
// edits_seen are not content: they describe how the set is sent.
func profileSetContentHash(doc *profileSetDoc) ([]byte, error) {
	items := make([]any, len(doc.Items))
	for i, item := range doc.Items {
		canonical := make(map[string]any, len(item.raw))
		for key, value := range item.raw {
			var decoded any
			_ = json.Unmarshal(value, &decoded)
			canonical[key] = decoded
		}
		canonical["pin"] = item.PIN.kind
		items[i] = canonical
	}
	encoded, err := json.Marshal(struct {
		Version int64 `json:"version"`
		Items   []any `json:"items"`
	}{doc.Version, items})
	if err != nil {
		return nil, fmt.Errorf("encoding the set's content: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return sum[:], nil
}
