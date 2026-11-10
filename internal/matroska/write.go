package matroska

import (
	"encoding/binary"
	"math"
)

// AppendID appends an element ID, which carries its own length marker.
func AppendID(dst []byte, id uint64) []byte {
	n := 1
	for n < maxIDLength && id >= uint64(1)<<(8*n) {
		n++
	}
	return appendLow(dst, id, n)
}

// appendLow appends v's n low bytes, big-endian.
func appendLow(dst []byte, v uint64, n int) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return append(dst, b[8-n:]...)
}

// AppendSize appends a data size in the shortest vint that holds it. A size
// whose bits are all ones in its shortest length takes one byte more, since
// all ones means an unknown size.
func AppendSize(dst []byte, size uint64) []byte {
	n := 1
	for n < 8 && size >= (uint64(1)<<(7*n))-1 {
		n++
	}
	return appendLow(dst, size|uint64(1)<<(7*n), n)
}

// AppendUnknownSize appends the 8-byte unknown size marker, for a master
// element written before its length is known.
func AppendUnknownSize(dst []byte) []byte {
	return append(dst, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
}

// AppendElement appends an element holding data.
func AppendElement(dst []byte, id uint64, data []byte) []byte {
	dst = AppendID(dst, id)
	dst = AppendSize(dst, uint64(len(data)))
	return append(dst, data...)
}

// AppendUint appends an unsigned integer element in as few bytes as it
// needs.
func AppendUint(dst []byte, id, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return AppendElement(dst, id, b[i:])
}

// AppendInt appends a signed integer element in as few bytes as it needs.
func AppendInt(dst []byte, id uint64, v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v)) //nolint:gosec // G115: two's complement is the encoding
	i := 0
	// Drop leading bytes that only repeat the sign of the next one.
	for i < 7 && ((b[i] == 0 && b[i+1]&0x80 == 0) || (b[i] == 0xFF && b[i+1]&0x80 != 0)) {
		i++
	}
	return AppendElement(dst, id, b[i:])
}

// AppendFloat appends an 8-byte float element.
func AppendFloat(dst []byte, id uint64, v float64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(v))
	return AppendElement(dst, id, b[:])
}

// AppendString appends a string element.
func AppendString(dst []byte, id uint64, s string) []byte {
	dst = AppendID(dst, id)
	dst = AppendSize(dst, uint64(len(s)))
	return append(dst, s...)
}
