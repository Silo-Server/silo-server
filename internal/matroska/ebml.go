// Package matroska holds the EBML and Matroska parsing primitives shared by
// the readers that work from a Matroska file's metadata and index without
// demuxing its media: track identity (mediaprobe), keyframe planning
// (playback/keyframes) and the cue-indexed subtitle reader (embeddedsub).
package matroska

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// UnknownSize is the data size of an element whose size field is all ones:
// it runs to the end of its parent.
const UnknownSize int64 = -1

// maxIDLength is the longest element ID Matroska allows (EBMLMaxIDLength).
const maxIDLength = 4

// ReadVint reads an EBML variable-length integer. IDs keep their length
// marker bit; sizes drop it.
func ReadVint(b []byte, keepMarker bool) (value uint64, length int, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	length = 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		length++
	}
	if len(b) < length {
		return 0, 0, false
	}
	value = uint64(b[0])
	if !keepMarker {
		value &= uint64(0xFF >> length)
	}
	for _, c := range b[1:length] {
		value = value<<8 | uint64(c)
	}
	return value, length, true
}

// ParseElementHeader decodes the element header at the start of b: its ID,
// its data size (UnknownSize when the size field is all ones) and the
// header's length. ok is false when b is too short to hold the header or
// the header is malformed.
func ParseElementHeader(b []byte) (id uint64, size int64, headerLen int, ok bool) {
	id, idLen, ok := ReadVint(b, true)
	if !ok || idLen > maxIDLength {
		return 0, 0, 0, false
	}
	raw, sizeLen, ok := ReadVint(b[idLen:], false)
	if !ok {
		return 0, 0, 0, false
	}
	if raw == (uint64(1)<<(7*sizeLen))-1 {
		return id, UnknownSize, idLen + sizeLen, true
	}
	return id, int64(raw), idLen + sizeLen, true //nolint:gosec // G115: an 8-byte size holds 56 bits
}

// MaxHeaderLen is the longest element header: a 4-byte ID and an 8-byte
// size.
const MaxHeaderLen = maxIDLength + 8

// ReadElementHeader reads the ID and data size of the element at off and
// returns the header's length. A size of UnknownSize means the element runs
// to the end of its parent. A header cut off by the end of the data, or a
// malformed one, reports io.ErrUnexpectedEOF; nothing at all at off reports
// io.EOF. Any other read error is returned as is.
func ReadElementHeader(r io.ReaderAt, off int64) (id uint64, size int64, headerLen int64, err error) {
	var buf [MaxHeaderLen]byte
	n, err := r.ReadAt(buf[:], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, 0, 0, err
	}
	if n == 0 {
		if err == nil {
			err = io.EOF
		}
		return 0, 0, 0, err
	}
	id, size, hl, ok := ParseElementHeader(buf[:n])
	if !ok {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	return id, size, int64(hl), nil
}

// ReadElementData reads size bytes of element data at off. A size that is
// unknown, negative or over limit is an error rather than an allocation.
func ReadElementData(r io.ReaderAt, off, size, limit int64) ([]byte, error) {
	if size < 0 || size > limit {
		return nil, fmt.Errorf("matroska: element at %d has unsupported size %d", off, size)
	}
	data := make([]byte, size)
	if _, err := r.ReadAt(data, off); err != nil {
		return nil, fmt.Errorf("matroska: read element at %d: %w", off, err)
	}
	return data, nil
}

// Children calls fn for each child element in a master element's data. It
// stops at the first malformed or unknown-size child.
func Children(data []byte, fn func(id uint64, payload []byte)) {
	ChildrenComplete(data, fn)
}

// ChildrenComplete is Children, reporting whether every child was well
// formed: false when malformed data stopped the walk early.
func ChildrenComplete(data []byte, fn func(id uint64, payload []byte)) bool {
	for len(data) > 0 {
		id, idLen, ok := ReadVint(data, true)
		if !ok {
			return false
		}
		raw, sizeLen, ok := ReadVint(data[idLen:], false)
		if !ok {
			return false
		}
		start := idLen + sizeLen
		if raw > uint64(len(data)-start) { //nolint:gosec // G115: ReadVint checked start <= len(data)
			return false
		}
		end := start + int(raw) //nolint:gosec // G115: raw is at most len(data)
		fn(id, data[start:end])
		data = data[end:]
	}
	return true
}

// Uint decodes an unsigned integer element. Matroska integers are at most 8
// bytes; a longer one decodes as 0.
func Uint(b []byte) uint64 {
	if len(b) > 8 {
		return 0
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// Int decodes a signed integer element, which is big-endian two's
// complement in 0 to 8 bytes.
func Int(b []byte) int64 {
	if len(b) == 0 || len(b) > 8 {
		return 0
	}
	v := int64(b[0])
	if b[0]&0x80 != 0 {
		v -= 0x100
	}
	for _, c := range b[1:] {
		v = v<<8 | int64(c)
	}
	return v
}

// Float decodes a 4- or 8-byte float element; any other length is 0.
func Float(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}

// String decodes an EBML string, which may be padded with trailing zero
// bytes.
func String(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
