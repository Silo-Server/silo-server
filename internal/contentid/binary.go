package contentid

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
)

// Reversible binary content_id codec. Pack encodes a structured or local
// content_id into at most 15 bytes; Unpack is its exact inverse. The Jellyfin
// compatibility layer uses this to pack a content_id into a client-facing UUID
// (1 kind byte + 15 payload bytes) with no side lookup table, so a compat item
// id decodes correctly even after a server restart (see
// docs/architecture/deterministic-content-id.md and internal/jellycompat).
//
// Byte 0 is a non-zero format tag. That is what lets the compat layer tell a
// packed content_id apart from the legacy numeric UUID encoding, whose
// corresponding byte is always zero.
//
//	movie / series: [tag][provider][uvarint digitCount][uvarint value]
//	season:         ... + [uvarint season]
//	episode:        ... + [uvarint season][uvarint episode]
//	local:          [tagLocal][localHashLen raw hash bytes]
//
// digitCount records the width of the provider id's numeric part so leading
// zeros survive (e.g. imdb tt0944947); value is that part as an integer. The
// structured forms are self-delimiting (uvarints), so the compat layer may
// zero-pad the packed bytes to fill the UUID and Unpack ignores the padding. The
// local form instead fills the payload exactly (1 + localHashLen == 15), so it
// needs no length prefix. Provider ids that overflow uint64 return ok=false, and
// the caller falls back to its opaque encoding.
const (
	tagMovie   byte = 0xC0
	tagSeries  byte = 0xC1
	tagSeason  byte = 0xC2
	tagEpisode byte = 0xC3
	tagLocal   byte = 0xC4
)

// localHashLen is the byte width of the local- path hash. It fills the compat
// UUID payload exactly after the 1-byte tag (1 + localHashLen == 15), which lets
// the local form round-trip without a length prefix. ForLocal emits exactly this
// many bytes; the two are in lockstep.
const localHashLen = 14

func providerToCode(p string) (byte, bool) {
	switch p {
	case ProviderTMDB:
		return 1, true
	case ProviderIMDB:
		return 2, true
	case ProviderTVDB:
		return 3, true
	}
	return 0, false
}

func codeToProvider(c byte) (string, bool) {
	switch c {
	case 1:
		return ProviderTMDB, true
	case 2:
		return ProviderIMDB, true
	case 3:
		return ProviderTVDB, true
	}
	return "", false
}

// Pack encodes a structured (movie/series/season/episode) or local content_id
// into its compact binary form. It returns ok=false for legacy numeric ids,
// provider ids too large for uint64, and anything malformed — the caller falls
// back to its numeric/opaque encoding for those.
func Pack(contentID string) ([]byte, bool) {
	id := strings.TrimSpace(contentID)

	if rest, ok := strings.CutPrefix(id, kindLocal+sep); ok {
		raw, err := hex.DecodeString(rest)
		if err != nil || len(raw) != localHashLen {
			return nil, false
		}
		out := make([]byte, 0, 1+localHashLen)
		out = append(out, tagLocal)
		out = append(out, raw...)
		return out, true
	}

	parts := strings.Split(id, sep)
	switch parts[0] {
	case kindMovie, kindSeries:
		if len(parts) != 3 {
			return nil, false
		}
		tag := tagMovie
		if parts[0] == kindSeries {
			tag = tagSeries
		}
		return packAnchored(tag, parts[1], parts[2], nil)
	case kindSeason:
		if len(parts) != 4 {
			return nil, false
		}
		nums, ok := parseNums(parts[3:])
		if !ok {
			return nil, false
		}
		return packAnchored(tagSeason, parts[1], parts[2], nums)
	case kindEpisode:
		if len(parts) != 5 {
			return nil, false
		}
		nums, ok := parseNums(parts[3:])
		if !ok {
			return nil, false
		}
		return packAnchored(tagEpisode, parts[1], parts[2], nums)
	}
	return nil, false
}

// Unpack reverses Pack. It fails closed (ok=false) on any byte sequence Pack
// could not have produced. Trailing zero padding after a complete structured id
// is ignored (the compat layer pads the packed bytes to fill the UUID); the
// fixed-length local form has no padding and is matched exactly.
func Unpack(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	tag, body := data[0], data[1:]

	if tag == tagLocal {
		// The local form fills the payload exactly, so its body must be exactly
		// localHashLen — reject any other length, including trailing bytes.
		if len(body) != localHashLen {
			return "", false
		}
		return kindLocal + sep + hex.EncodeToString(body), true
	}

	var kind string
	var nNums int
	switch tag {
	case tagMovie:
		kind = kindMovie
	case tagSeries:
		kind = kindSeries
	case tagSeason:
		kind, nNums = kindSeason, 1
	case tagEpisode:
		kind, nNums = kindEpisode, 2
	default:
		return "", false
	}

	if len(body) == 0 {
		return "", false
	}
	provider, ok := codeToProvider(body[0])
	if !ok {
		return "", false
	}
	body = body[1:]

	digitCount, value, body, ok := readUvarint2(body)
	if !ok || digitCount == 0 || digitCount > 64 {
		return "", false
	}

	nums := make([]uint64, nNums)
	for i := range nums {
		v, n := binary.Uvarint(body)
		if n <= 0 {
			return "", false
		}
		nums[i] = v
		body = body[n:]
	}

	digits := padDigits(value, int(digitCount))
	idStr := digits
	if provider == ProviderIMDB {
		idStr = "tt" + digits
	}

	var sb strings.Builder
	sb.WriteString(kind)
	sb.WriteString(sep)
	sb.WriteString(provider)
	sb.WriteString(sep)
	sb.WriteString(idStr)
	for _, nm := range nums {
		sb.WriteString(sep)
		sb.WriteString(strconv.FormatUint(nm, 10))
	}
	return sb.String(), true
}

// packAnchored serializes a provider-anchored content_id's components. nums
// carries the trailing season (and episode) numbers, if any.
func packAnchored(tag byte, provider, idStr string, nums []uint64) ([]byte, bool) {
	code, ok := providerToCode(provider)
	if !ok {
		return nil, false
	}
	normalized, ok := normalizeProviderID(provider, idStr)
	if !ok {
		return nil, false
	}
	digits := normalized
	if provider == ProviderIMDB {
		digits = strings.TrimPrefix(normalized, "tt")
	}
	value, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return nil, false // too large for uint64 — caller falls back
	}
	out := make([]byte, 0, 16)
	out = append(out, tag, code)
	out = binary.AppendUvarint(out, uint64(len(digits)))
	out = binary.AppendUvarint(out, value)
	for _, n := range nums {
		out = binary.AppendUvarint(out, n)
	}
	return out, true
}

func parseNums(strs []string) ([]uint64, bool) {
	out := make([]uint64, len(strs))
	for i, s := range strs {
		if !numericIDPattern.MatchString(s) {
			return nil, false
		}
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

// readUvarint2 reads two consecutive uvarints, returning the remaining bytes.
func readUvarint2(body []byte) (a, b uint64, rest []byte, ok bool) {
	a, n := binary.Uvarint(body)
	if n <= 0 {
		return 0, 0, nil, false
	}
	body = body[n:]
	b, n = binary.Uvarint(body)
	if n <= 0 {
		return 0, 0, nil, false
	}
	return a, b, body[n:], true
}

// padDigits renders v as a base-10 string left-padded with zeros to width.
func padDigits(v uint64, width int) string {
	s := strconv.FormatUint(v, 10)
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}
