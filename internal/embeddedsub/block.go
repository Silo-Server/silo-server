package embeddedsub

import (
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// errCorrupt marks data that isn't what the index says is there; reads that
// fail for it are reported as damage, unlike a failed read.
var errCorrupt = errors.New("corrupt")

func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCorrupt, fmt.Sprintf(format, args...))
}

// maxPacketSize bounds a decompressed packet. FFmpeg gives up on zlib
// packets near 10 MB; the largest PGS display sets are a few MB.
const maxPacketSize = 16 << 20

// matroskaBlock is a parsed SimpleBlock or BlockGroup.
type matroskaBlock struct {
	track uint64
	// rel is the timestamp relative to the cluster's, in ticks.
	rel      int16
	keyframe bool
	frames   [][]byte
	// duration is the BlockDuration in ticks, when hasDuration.
	duration    int64
	hasDuration bool
}

// parseBlockElement parses the data of a SimpleBlock or BlockGroup.
func parseBlockElement(id uint64, data []byte) (matroskaBlock, error) {
	switch id {
	case matroska.IDSimpleBlock:
		b, flags, err := parseBlock(data)
		b.keyframe = flags&0x80 != 0
		return b, err
	case matroska.IDBlockGroup:
		var (
			blockData   []byte
			found       bool
			references  int
			duration    uint64
			hasDuration bool
		)
		if !matroska.ChildrenComplete(data, func(id uint64, v []byte) {
			switch id {
			case matroska.IDBlock:
				blockData, found = v, true
			case matroska.IDBlockDuration:
				duration, hasDuration = matroska.Uint(v), true
			case matroska.IDReferenceBlock:
				references++
			}
		}) {
			return matroskaBlock{}, corrupt("malformed block group")
		}
		if !found {
			return matroskaBlock{}, corrupt("block group without a block")
		}
		b, _, err := parseBlock(blockData)
		// FFmpeg takes a block group without references as a keyframe.
		b.keyframe = references == 0
		if hasDuration && duration <= math.MaxInt64 {
			b.duration, b.hasDuration = int64(duration), true
		}
		return b, err
	}
	return matroskaBlock{}, corrupt("element %#x is not a block", id)
}

// parseBlock parses a Block or SimpleBlock body: track number, relative
// timestamp, flags and frames, following FFmpeg's matroska_parse_laces.
func parseBlock(b []byte) (matroskaBlock, byte, error) {
	track, n, ok := matroska.ReadVint(b, false)
	if !ok || len(b) < n+3 {
		return matroskaBlock{}, 0, corrupt("short block header")
	}
	blk := matroskaBlock{track: track, rel: int16(b[n])<<8 | int16(b[n+1])}
	flags := b[n+2]
	data := b[n+3:]
	lacing := (flags >> 1) & 3
	if lacing == 0 {
		blk.frames = [][]byte{data}
		return blk, flags, nil
	}
	if len(data) == 0 {
		return matroskaBlock{}, 0, corrupt("laced block without a lace count")
	}
	laces := int(data[0]) + 1
	data = data[1:]
	sizes := make([]int, laces)
	switch lacing {
	case 1: // Xiph
		total := 0
		for i := range laces - 1 {
			for {
				if len(data) <= total {
					return matroskaBlock{}, 0, corrupt("Xiph lace sizes run past the block")
				}
				c := int(data[0])
				data = data[1:]
				sizes[i] += c
				total += c
				if c != 0xFF {
					break
				}
			}
		}
		if len(data) < total {
			return matroskaBlock{}, 0, corrupt("Xiph laces run past the block")
		}
		sizes[laces-1] = len(data) - total
	case 2: // fixed
		if len(data)%laces != 0 {
			return matroskaBlock{}, 0, corrupt("fixed laces don't divide the block")
		}
		for i := range sizes {
			sizes[i] = len(data) / laces
		}
	case 3: // EBML
		first, l, ok := matroska.ReadVint(data, false)
		if !ok || first > math.MaxInt32 {
			return matroskaBlock{}, 0, corrupt("bad EBML lace size")
		}
		data = data[l:]
		sizes[0] = int(first)
		total := sizes[0]
		for i := 1; i < laces-1; i++ {
			raw, l, ok := matroska.ReadVint(data, false)
			if !ok || raw > math.MaxInt32 {
				return matroskaBlock{}, 0, corrupt("bad EBML lace size")
			}
			data = data[l:]
			delta := int64(raw) - (int64(1)<<(7*l-1) - 1)
			size := int64(sizes[i-1]) + delta
			if size < 0 || size > math.MaxInt32 {
				return matroskaBlock{}, 0, corrupt("bad EBML lace size")
			}
			sizes[i] = int(size)
			total += sizes[i]
		}
		if len(data) < total {
			return matroskaBlock{}, 0, corrupt("EBML laces run past the block")
		}
		sizes[laces-1] = len(data) - total
	}
	blk.frames = make([][]byte, laces)
	for i, size := range sizes {
		blk.frames[i], data = data[:size], data[size:]
	}
	return blk, flags, nil
}

// decompress undoes a track's ContentCompression, as FFmpeg's
// matroska_decode_buffer does.
func decompress(c *Compression, data []byte) ([]byte, error) {
	switch c.Algorithm {
	case matroska.CompressionHeaderStripping:
		out := make([]byte, 0, len(c.Settings)+len(data))
		return append(append(out, c.Settings...), data...), nil
	case matroska.CompressionZlib:
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, corrupt("zlib: %v", err)
		}
		return readDecompressed(zr, "zlib")
	case matroska.CompressionBzlib:
		return readDecompressed(bzip2.NewReader(bytes.NewReader(data)), "bzlib")
	}
	return nil, corrupt("compression algorithm %d", c.Algorithm)
}

func readDecompressed(r io.Reader, name string) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, maxPacketSize+1))
	if err != nil {
		return nil, corrupt("%s: %v", name, err)
	}
	if len(out) > maxPacketSize {
		return nil, corrupt("%s packet over %d bytes", name, maxPacketSize)
	}
	return out, nil
}
