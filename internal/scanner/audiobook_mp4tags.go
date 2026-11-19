package scanner

import (
	"bytes"
	"encoding/binary"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// mp4BookAtomTags maps iTunes item-list atoms that ffprobe (through FFmpeg
// 8.1) does not expose to the tag keys populateFromTags reads. Audiobook
// taggers write the series as a movement (©mvn name, ©mvi number), and some
// write the narrator and publisher to ©nrt and ©pub.
//
//nolint:goconst // Tag keys are data, matching the names ffprobe reports.
var mp4BookAtomTags = map[string]string{
	"\xa9mvn": "mvnm",
	"\xa9mvi": "mvin",
	"\xa9nrt": "narrator",
	"\xa9pub": "publisher",
}

const (
	// mp4MaxBoxesPerLevel bounds the walk through a damaged or hostile file.
	mp4MaxBoxesPerLevel = 512
	// mp4MaxTagValue bounds one text value; these atoms hold short names.
	mp4MaxTagValue = 4096
	// mp4DataTypeUTF8 and mp4DataTypeInteger are the iTunes "data" atom
	// well-known types for a UTF-8 string and a big-endian signed integer.
	mp4DataTypeUTF8    = 1
	mp4DataTypeInteger = 21
)

// readMP4BookAtomTags reads the atoms in mp4BookAtomTags from an MP4/M4B
// file's moov/udta/meta/ilst. It reads box headers and the few small values
// it wants, so mdat (the audio) is never read. Any parse problem yields the
// values found so far: these tags only add to what ffprobe reported.
func readMP4BookAtomTags(path string) map[string]string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4b", ".m4a", ".mp4": //nolint:goconst // the MP4 family of audio extensions
	default:
		return nil
	}
	f, err := fspath.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	return readMP4ItemList(f, info.Size())
}

func readMP4ItemList(r io.ReaderAt, size int64) map[string]string {
	moov, ok := findMP4Box(r, 0, size, "moov")
	if !ok {
		return nil
	}
	// iTunes item lists live in moov/udta/meta.
	meta, ok := mp4Path(r, moov, "udta", "meta")
	if !ok {
		return nil
	}
	// meta is a full box: a 4-byte version and flags precede its children.
	meta.start += 4
	ilst, ok := findMP4Box(r, meta.start, meta.end, "ilst")
	if !ok {
		return nil
	}
	out := make(map[string]string)
	walkMP4Boxes(r, ilst.start, ilst.end, func(boxType string, body mp4Range) bool {
		key, wanted := mp4BookAtomTags[boxType]
		if !wanted {
			return true
		}
		if value := readMP4DataValue(r, body); value != "" {
			out[key] = value
		}
		return true
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

type mp4Range struct{ start, end int64 }

func mp4Path(r io.ReaderAt, parent mp4Range, types ...string) (mp4Range, bool) {
	cur := parent
	for _, t := range types {
		next, ok := findMP4Box(r, cur.start, cur.end, t)
		if !ok {
			return mp4Range{}, false
		}
		cur = next
	}
	return cur, true
}

func findMP4Box(r io.ReaderAt, start, end int64, want string) (mp4Range, bool) {
	var found mp4Range
	var ok bool
	walkMP4Boxes(r, start, end, func(boxType string, body mp4Range) bool {
		if boxType == want {
			found, ok = body, true
			return false
		}
		return true
	})
	return found, ok
}

// walkMP4Boxes calls fn with the type and body range of each box in
// [start, end) until fn returns false or a header does not fit its parent.
func walkMP4Boxes(r io.ReaderAt, start, end int64, fn func(boxType string, body mp4Range) bool) {
	var hdr [16]byte
	pos := start
	for range mp4MaxBoxesPerLevel {
		if end-pos < 8 {
			return
		}
		if _, err := r.ReadAt(hdr[:8], pos); err != nil {
			return
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		boxType := string(hdr[4:8])
		headerLen := int64(8)
		switch size {
		case 0: // extends to the end of the parent
			size = end - pos
		case 1: // 64-bit size follows the type
			if end-pos < 16 {
				return
			}
			if _, err := r.ReadAt(hdr[8:16], pos+8); err != nil {
				return
			}
			if err := binary.Read(bytes.NewReader(hdr[8:16]), binary.BigEndian, &size); err != nil {
				return
			}
			headerLen = 16
		}
		if size < headerLen || size > end-pos {
			return
		}
		if !fn(boxType, mp4Range{start: pos + headerLen, end: pos + size}) {
			return
		}
		pos += size
	}
}

// readMP4DataValue reads the first "data" child of an item-list entry.
func readMP4DataValue(r io.ReaderAt, item mp4Range) string {
	data, ok := findMP4Box(r, item.start, item.end, "data")
	if !ok || data.end-data.start < 8 {
		return ""
	}
	var head [8]byte // 1-byte version, 3-byte type, 4-byte locale
	if _, err := r.ReadAt(head[:], data.start); err != nil {
		return ""
	}
	dataType := int(head[1])<<16 | int(head[2])<<8 | int(head[3])
	n := data.end - data.start - 8
	if n <= 0 || n > mp4MaxTagValue {
		return ""
	}
	payload := make([]byte, n)
	if _, err := r.ReadAt(payload, data.start+8); err != nil {
		return ""
	}
	switch dataType {
	case mp4DataTypeUTF8:
		if !utf8.Valid(payload) {
			return ""
		}
		return strings.TrimSpace(strings.TrimRight(string(payload), "\x00"))
	case mp4DataTypeInteger, 0:
		v, ok := readMP4Integer(payload)
		if !ok || v <= 0 {
			return ""
		}
		return strconv.FormatInt(v, 10)
	}
	return ""
}

// readMP4Integer reads a big-endian signed integer of 1, 2, 4, or 8 bytes.
func readMP4Integer(payload []byte) (int64, bool) {
	r := bytes.NewReader(payload)
	switch len(payload) {
	case 1:
		var v int8
		err := binary.Read(r, binary.BigEndian, &v)
		return int64(v), err == nil
	case 2:
		var v int16
		err := binary.Read(r, binary.BigEndian, &v)
		return int64(v), err == nil
	case 4:
		var v int32
		err := binary.Read(r, binary.BigEndian, &v)
		return int64(v), err == nil
	case 8:
		var v int64
		err := binary.Read(r, binary.BigEndian, &v)
		return v, err == nil
	}
	return 0, false
}
