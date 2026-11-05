package backup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// maxPAXRecord bounds a PAX extended header: the writer emits one for a
// long name or a precise time, a few hundred bytes.
const maxPAXRecord = 8 << 10

// errTarShape refuses an archive entry the writer never makes.
var errTarShape = errors.New("archive entry is not one a Silo backup holds")

// tarGuard sits between an archive's bytes and tar.Reader and checks each
// raw 512-byte header before tar.Reader sees it. tar.Reader expands
// extension records (old GNU sparse maps, long names, PAX headers) inside
// Next, before the caller can look at the entry, so a hostile archive could
// make one Next consume and allocate without bound. The writer makes only
// regular files, with a PAX record ('x') of a few hundred bytes where
// needed; anything else is refused here, and so a Next consumes at most a
// PAX record and two headers beyond the entry it returns. After the end
// marker the bytes pass through unread, for the caller's trailer check.
type tarGuard struct {
	r io.Reader
	// pending is a checked header not yet passed on; remaining the entry
	// data, padded to whole blocks, still to pass through.
	pending   []byte
	remaining int64
	ended     bool
	// sizeOverride is the size a PAX record gave the next entry; entrySize
	// the size the guard let the last entry's data run for, which
	// tar.Reader must agree with (checkEntry).
	sizeOverride int64
	entrySize    int64
}

func newTarGuard(r io.Reader) *tarGuard { return &tarGuard{r: r, sizeOverride: -1} }

func (g *tarGuard) Read(p []byte) (int, error) {
	if len(g.pending) > 0 {
		n := copy(p, g.pending)
		g.pending = g.pending[n:]
		return n, nil
	}
	if g.ended {
		return g.r.Read(p)
	}
	if g.remaining > 0 {
		n, err := g.r.Read(p[:min(int64(len(p)), g.remaining)])
		g.remaining -= int64(n)
		return n, err
	}
	block := make([]byte, 512)
	if _, err := io.ReadFull(g.r, block); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	if bytes.Equal(block, make([]byte, 512)) {
		// The end marker; what follows is the caller's.
		g.ended = true
		g.pending = block
		return g.Read(p)
	}
	size, err := checkTarHeader(block)
	if err != nil {
		return 0, err
	}
	if block[156] == 'x' {
		// The record is read whole (it is small) to learn the size it gives
		// the entry that follows, which the header's field may not hold.
		record := make([]byte, (size+511)/512*512)
		if _, err := io.ReadFull(g.r, record); err != nil {
			return 0, err
		}
		if g.sizeOverride, err = paxSize(record[:size]); err != nil {
			return 0, err
		}
		g.pending = slices.Concat(block, record)
		return g.Read(p)
	}
	if g.sizeOverride >= 0 {
		size = g.sizeOverride
	}
	g.sizeOverride = -1
	g.pending, g.remaining, g.entrySize = block, (size+511)/512*512, size
	return g.Read(p)
}

// checkEntry requires the entry tar.Reader's Next returned to have the
// size the guard let its data run for: were they to differ, the reader
// would take what the guard passed as data for headers it never checked.
func (g *tarGuard) checkEntry(size int64) error {
	if size != g.entrySize {
		return fmt.Errorf("%w: its size reads as %d bytes, not %d", errTarShape, size, g.entrySize)
	}
	return nil
}

// paxKeys are the PAX records the writer emits for a regular file (Go's
// archive/tar writer in PAX format): a long or non-ASCII name, a size its
// header cannot hold, and a precise modification time.
var paxKeys = map[string]bool{"path": true, "size": true, "mtime": true}

// paxSize finds the size a PAX record sets, or -1 for none. It refuses a
// key the writer never emits (paxKeys) and any key given twice: tar.Reader
// keeps a repeated key's last value, so a guard that read another would
// disagree with it about where the entry's data ends.
func paxSize(record []byte) (int64, error) {
	size := int64(-1)
	seen := map[string]bool{}
	for len(record) > 0 {
		space := bytes.IndexByte(record, ' ')
		if space < 1 {
			return 0, fmt.Errorf("%w: a malformed PAX record", errTarShape)
		}
		n, err := strconv.Atoi(string(record[:space]))
		if err != nil || n <= space || n > len(record) || record[n-1] != '\n' {
			return 0, fmt.Errorf("%w: a malformed PAX record", errTarShape)
		}
		key, value, ok := strings.Cut(string(record[space+1:n-1]), "=")
		if !ok || !paxKeys[key] || seen[key] {
			return 0, fmt.Errorf("%w: a PAX record %q", errTarShape, key)
		}
		seen[key] = true
		if key == "size" {
			if size, err = strconv.ParseInt(value, 10, 64); err != nil || size < 0 {
				return 0, fmt.Errorf("%w: a PAX record with an invalid size", errTarShape)
			}
		}
		record = record[n:]
	}
	return size, nil
}

// checkTarHeader accepts a regular file's header or a small PAX record's,
// and returns the size of the data that follows.
func checkTarHeader(block []byte) (int64, error) {
	size, err := tarOctal(block[124:136])
	if err != nil || size < 0 {
		return 0, fmt.Errorf("%w: a header has no valid size", errTarShape)
	}
	switch typeflag := block[156]; typeflag {
	case '0', 0:
		return size, nil
	case 'x':
		if size > maxPAXRecord {
			return 0, fmt.Errorf("%w: a PAX record of %d bytes", errTarShape, size)
		}
		return size, nil
	default:
		return 0, fmt.Errorf("%w: type %q", errTarShape, typeflag)
	}
}

// tarOctal reads a header's octal number field; the writer's sizes fit
// its digits (it records larger ones in a PAX record).
func tarOctal(field []byte) (int64, error) {
	s := strings.TrimRight(strings.TrimSpace(string(bytes.TrimRight(field, "\x00"))), "\x00")
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 8, 64)
}
