package scanner

import (
	"encoding/binary"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

const (
	// oshashBlockSize is the number of bytes read from the start and end of a file.
	oshashBlockSize = 65536
	// oshashMinFileSize is the minimum file size required for OSHash (2 * blockSize).
	oshashMinFileSize = oshashBlockSize * 2
	// oshashUint64Count is the number of uint64 values in one block.
	oshashUint64Count = oshashBlockSize / 8
)

// ComputeOSHash computes the OpenSubtitles hash of the file at the given path.
// The result is a 16-character lowercase hexadecimal string (zero-padded).
//
// Algorithm:
//  1. Open file, get size.
//  2. Read first 65536 bytes as array of uint64 (little-endian), sum them.
//  3. Read last 65536 bytes as array of uint64 (little-endian), sum them.
//  4. Add file size to the sum.
//  5. Format as 16-character lowercase hex (zero-padded).
//
// Files smaller than 128KB (65536 * 2) return an error.
func ComputeOSHash(path string) (string, error) {
	f, err := fspath.Open(path)
	if err != nil {
		return "", fmt.Errorf("oshash: open %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("oshash: stat %s: %w", path, err)
	}

	fileSize := fi.Size()
	if fileSize < oshashMinFileSize {
		return "", fmt.Errorf("oshash: file %s is too small (%d bytes, minimum %d)", path, fileSize, oshashMinFileSize)
	}

	var hash uint64

	// Read first 65536 bytes.
	buf := make([]byte, oshashBlockSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return "", fmt.Errorf("oshash: read head of %s: %w", path, err)
	}
	hash += sumUint64Block(buf)

	// Read last 65536 bytes.
	if _, err := f.ReadAt(buf, fileSize-oshashBlockSize); err != nil {
		return "", fmt.Errorf("oshash: read tail of %s: %w", path, err)
	}
	hash += sumUint64Block(buf)

	// Add file size.
	hash += uint64(fileSize)

	return fmt.Sprintf("%016x", hash), nil
}

// sumUint64Block interprets a byte buffer as an array of little-endian uint64
// values and returns their sum. The buffer length must be a multiple of 8.
func sumUint64Block(buf []byte) uint64 {
	var sum uint64
	for i := 0; i < len(buf); i += 8 {
		sum += binary.LittleEndian.Uint64(buf[i : i+8])
	}
	return sum
}
