package playback

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
)

// A segment response must be the whole segment or a retryable refusal. The
// lease's size alone cannot promise that: an open only proves the name exists
// with bytes, so a name a writer is still filling is served as if it were
// finished, and the client appends a fragment that yields no frames.
//
// Two conditions make a name unusable, and openSegment refuses both:
//
//   - The HLS muxer holds "<name>.tmp" while it produces that index. The final
//     name either does not exist yet, or still holds the previous generation's
//     bytes that a restart preserved for the same emitted recipe. Either way the
//     writer has not settled the name, so it is not served from a snapshot the
//     writer is about to replace.
//   - FFmpeg truncates and rewrites fMP4 init.mp4 in place (no temp file), so a
//     request that races a restart can read a prefix of it. init.mp4 is the one
//     segment whose packaging can be proved whole from its bytes: the ISO-BMFF
//     box walk below requires every declared box to fit and the moov to exist,
//     which a prefix cannot satisfy.

// segmentTempInProgress reports whether the writer still holds the temp file
// for name. root is the session's output directory opened as an os.Root.
func segmentTempInProgress(root *os.Root, name string) bool {
	info, err := root.Stat(name + ".tmp")
	return err == nil && info.Mode().IsRegular()
}

// initSegmentComplete reports whether the opened init segment is a whole
// ISO-BMFF file carrying its moov box.
func initSegmentComplete(name string, f *os.File, size int64) error {
	if !strings.EqualFold(name, hlsInitSegmentName) {
		return nil
	}
	return isoBMFFComplete(f, size, "moov")
}

// isoBMFFBoxWalkLimit bounds the boxes one segment may declare. A real init
// segment has a handful at top level; the limit only stops a corrupt header
// from spinning the walk.
const isoBMFFBoxWalkLimit = 4096

// isoBMFFComplete walks the top-level boxes and reports whether they exactly
// cover size and include every required box type. A file cut inside a box fails
// on that box's declared size; a prefix cut between boxes fails on the missing
// required box.
func isoBMFFComplete(f *os.File, size int64, required ...string) error {
	if size <= 0 {
		return ErrSegmentIncomplete
	}
	found := make(map[string]bool, len(required))
	var offset int64
	for boxes := 0; offset < size; boxes++ {
		if boxes >= isoBMFFBoxWalkLimit {
			return ErrSegmentIncomplete
		}
		var header [8]byte
		if _, err := f.ReadAt(header[:], offset); err != nil {
			return ErrSegmentIncomplete
		}
		boxSize := int64(binary.BigEndian.Uint32(header[0:4]))
		boxType := string(header[4:8])
		headerLen := int64(8)
		switch boxSize {
		case 0:
			// The box runs to the end of the file, so it must be the last one.
			boxSize = size - offset
		case 1:
			var large [8]byte
			if _, err := f.ReadAt(large[:], offset+8); err != nil {
				return ErrSegmentIncomplete
			}
			declared := binary.BigEndian.Uint64(large[:])
			if declared > uint64(size-offset) {
				return ErrSegmentIncomplete
			}
			boxSize = int64(declared) //nolint:gosec // bounded by the remaining file size above.
			headerLen = 16
		}
		if boxSize < headerLen || boxSize > size-offset {
			return ErrSegmentIncomplete
		}
		for _, want := range required {
			if boxType == want {
				found[want] = true
			}
		}
		offset += boxSize
	}
	if offset != size {
		return ErrSegmentIncomplete
	}
	for _, want := range required {
		if !found[want] {
			return ErrSegmentIncomplete
		}
	}
	return nil
}

// removeStaleSegmentTemps deletes the HLS muxer's temp files after the process
// that owned them is gone. A restart kills FFmpeg between a temp file's open
// and its rename, and the leftover would otherwise make the final name look
// like a live writer for the rest of the session.
func (s *TranscodeSession) removeStaleSegmentTemps() {
	entries, err := os.ReadDir(s.outputDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "seg_") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		if _, parseErr := ParseSegmentNumber(strings.TrimSuffix(name, ".tmp")); parseErr != nil {
			continue
		}
		_ = os.Remove(filepath.Join(s.outputDir, name))
	}
}
