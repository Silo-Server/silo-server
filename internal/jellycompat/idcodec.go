package jellycompat

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"sync"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/contentid"
)

// EncodedIDType distinguishes packed compat UUIDs.
type EncodedIDType byte

const (
	EncodedIDLibrary     EncodedIDType = 1
	EncodedIDItem        EncodedIDType = 2
	EncodedIDMediaSource EncodedIDType = 3
	EncodedIDSeason      EncodedIDType = 4
	EncodedIDPlaySession EncodedIDType = 5
	EncodedIDGenre       EncodedIDType = 6
	EncodedIDStudio      EncodedIDType = 7
	EncodedIDPerson      EncodedIDType = 8
	EncodedIDImageProxy  EncodedIDType = 9
	EncodedIDCollection  EncodedIDType = 10
)

var (
	pseudoUserNamespace = uuid.MustParse("3dfcc388-bf95-5572-bc16-7f1a375992dd")
	stringIDNamespaces  = map[EncodedIDType]uuid.UUID{
		EncodedIDItem:        uuid.MustParse("0b6716ca-1f61-5987-b17b-f592f04fd6b3"),
		EncodedIDSeason:      uuid.MustParse("29831b2b-dad5-5a85-b506-4d1fb2da01ed"),
		EncodedIDPlaySession: uuid.MustParse("75a69ca8-f95f-5e9d-ac0a-d34a37b93eb4"),
		EncodedIDGenre:       uuid.MustParse("c0cbb8ea-8331-52c0-b160-15e7cf899fb0"),
		EncodedIDStudio:      uuid.MustParse("23712982-b769-592d-9360-b4d3f39654db"),
		EncodedIDPerson:      uuid.MustParse("a4e7c1d6-3b8f-5a2e-9c01-7d6f4e8b2a13"),
		EncodedIDCollection:  uuid.MustParse("7f3c2a91-5b64-5c1d-8e07-9a2f4d6b1c35"),
	}
)

// DecodedID is a packed compat UUID decoded back to its type and value.
type DecodedID struct {
	Type  EncodedIDType
	Value uint64
}

// ResourceIDCodec encodes numeric IDs directly and keeps reversible mappings
// for opaque string content IDs used by media items and seasons.
type ResourceIDCodec struct {
	mu                sync.RWMutex
	reverse           map[string]registeredID
	mediaSourceOwners map[int64]string
}

type registeredID struct {
	kind  EncodedIDType
	value string
}

// PseudoUserID deterministically derives the Jellyfin pseudo-user UUID.
func PseudoUserID(userID int, profileID string) uuid.UUID {
	return uuid.NewSHA1(pseudoUserNamespace, fmt.Appendf(nil, "%d:%s", userID, profileID))
}

// NewResourceIDCodec creates a new route ID codec.
func NewResourceIDCodec() *ResourceIDCodec {
	return &ResourceIDCodec{
		reverse:           make(map[string]registeredID),
		mediaSourceOwners: make(map[int64]string),
	}
}

// EncodeNumericID packs a numeric Silo identifier into a UUID.
func EncodeNumericID(kind EncodedIDType, value uint64) uuid.UUID {
	var raw [16]byte
	raw[0] = byte(kind)
	binary.BigEndian.PutUint64(raw[8:], value)
	return uuid.UUID(raw)
}

// EncodeStringID encodes a Silo identifier into a Jellyfin UUID string.
//
// Content-id kinds (item, season) whose value is a structured or local
// content_id are packed into the UUID reversibly (see contentid.Pack), so they
// decode with no lookup table and survive a server restart. Numeric ids keep
// their stateless numeric packing. Everything else — arbitrary names such as
// genres and studios, and the rare content_id too large to pack — falls back to
// a hashed UUID recorded in the reverse map.
func (c *ResourceIDCodec) EncodeStringID(kind EncodedIDType, value string) string {
	if numeric, err := strconv.ParseUint(value, 10, 64); err == nil {
		return EncodeNumericID(kind, numeric).String()
	}

	if isContentIDKind(kind) {
		if packed, ok := packContentIDUUID(kind, value); ok {
			return packed.String()
		}
	}

	namespace, ok := stringIDNamespaces[kind]
	if !ok {
		namespace = uuid.NameSpaceURL
	}
	encoded := uuid.NewSHA1(namespace, []byte(value))

	c.mu.Lock()
	c.reverse[encoded.String()] = registeredID{kind: kind, value: value}
	c.mu.Unlock()

	return encoded.String()
}

// EncodeIntID encodes a native integer ID into a Jellyfin UUID string.
func (c *ResourceIDCodec) EncodeIntID(kind EncodedIDType, value int64) string {
	return EncodeNumericID(kind, uint64(value)).String()
}

// DecodeStringID decodes a compat UUID back to the original native string ID.
//
// A reversibly packed content_id is decoded first (and re-packed to confirm the
// UUID genuinely came from the packer, so an opaque id whose bytes merely happen
// to parse is rejected), then the stateless numeric encoding, then the reverse
// map for hashed ids.
func (c *ResourceIDCodec) DecodeStringID(kind EncodedIDType, raw string) (string, error) {
	if isContentIDKind(kind) {
		if id, ok := unpackContentIDUUID(kind, raw); ok {
			return id, nil
		}
	}

	if decoded, err := DecodeID(raw); err == nil && decoded.Type == kind {
		return strconv.FormatUint(decoded.Value, 10), nil
	}

	c.mu.RLock()
	registered, ok := c.reverse[raw]
	c.mu.RUnlock()
	if !ok || registered.kind != kind {
		return "", fmt.Errorf("unknown compat id %q", raw)
	}
	return registered.value, nil
}

// isContentIDKind reports whether a compat id kind carries a Silo content_id (as
// opposed to an arbitrary name like a genre or studio). Only these kinds use the
// reversible content_id packing; everything else keeps the opaque hash + map.
func isContentIDKind(kind EncodedIDType) bool {
	return kind == EncodedIDItem || kind == EncodedIDSeason
}

// packContentIDUUID packs a structured or local content_id into a compat UUID:
// byte 0 is the kind and bytes 1..15 hold contentid.Pack output, zero-padded.
// The pack format tag at byte 1 is always non-zero, which distinguishes a packed
// UUID from the numeric encoding (whose byte 1 is zero). ok=false when the id
// does not pack within the 15-byte payload.
func packContentIDUUID(kind EncodedIDType, contentID string) (uuid.UUID, bool) {
	payload, ok := contentid.Pack(contentID)
	if !ok || len(payload) > 15 {
		return uuid.UUID{}, false
	}
	var u uuid.UUID
	u[0] = byte(kind)
	copy(u[1:], payload)
	return u, true
}

// unpackContentIDUUID reverses packContentIDUUID. It re-packs the decoded id and
// compares, so a UUID that was not produced by the packer (e.g. an opaque SHA1
// id whose bytes happen to parse) is rejected and the caller falls through to
// the reverse map.
func unpackContentIDUUID(kind EncodedIDType, raw string) (string, bool) {
	u, err := uuid.Parse(raw)
	if err != nil || u[0] != byte(kind) || u[1] == 0 {
		return "", false
	}
	id, ok := contentid.Unpack(u[1:])
	if !ok {
		return "", false
	}
	if check, ok := packContentIDUUID(kind, id); !ok || check != u {
		return "", false
	}
	return id, true
}

// DecodeIntID decodes a compat UUID back to a native integer ID.
func (c *ResourceIDCodec) DecodeIntID(kind EncodedIDType, raw string) (int64, error) {
	decoded, err := DecodeID(raw)
	if err != nil {
		return 0, err
	}
	if decoded.Type != kind {
		return 0, fmt.Errorf("unexpected compat id type %d", decoded.Type)
	}
	return int64(decoded.Value), nil
}

// RegisterMediaSourceOwner records which content item owns a media-source/file ID.
func (c *ResourceIDCodec) RegisterMediaSourceOwner(fileID int64, contentID string) {
	c.mu.Lock()
	c.mediaSourceOwners[fileID] = contentID
	c.mu.Unlock()
}

// LookupMediaSourceOwner resolves a media-source/file ID back to its content item.
func (c *ResourceIDCodec) LookupMediaSourceOwner(fileID int64) (string, bool) {
	c.mu.RLock()
	contentID, ok := c.mediaSourceOwners[fileID]
	c.mu.RUnlock()
	return contentID, ok
}

// mediaSourceIDsEqual reports whether two media-source IDs refer to the same
// source, tolerating UUID format differences. Silo exposes the canonical
// dashed compat UUID (e.g. "03000000-0000-0000-0000-00000019e8c2"), but some
// Jellyfin clients (e.g. Wholphin) echo it back in the compact 32-char hex
// form ("0300000000000000000000000019e8c2"). Both parse to the same UUID, so
// matching must compare the parsed values rather than the raw strings.
func mediaSourceIDsEqual(a, b string) bool {
	if a == b {
		return true
	}
	ua, errA := uuid.Parse(a)
	ub, errB := uuid.Parse(b)
	return errA == nil && errB == nil && ua == ub
}

// DecodeID unpacks a compat UUID into its original numeric value.
func DecodeID(raw string) (DecodedID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return DecodedID{}, fmt.Errorf("parse uuid: %w", err)
	}

	return DecodedID{
		Type:  EncodedIDType(parsed[0]),
		Value: binary.BigEndian.Uint64(parsed[8:]),
	}, nil
}
