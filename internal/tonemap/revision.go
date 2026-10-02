package tonemap

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrSourceRevisionChanged reports that live source facts no longer match the
// frozen tone-map recipe.
var ErrSourceRevisionChanged = errors.New("tone-map source revision changed")

// SourceRevision freezes the catalog and filesystem facts used to validate a
// tone-map recipe. It is carried with the recipe so seeks, node restarts, and
// prepared downloads cannot silently apply a verdict to replacement bytes.
type SourceRevision struct {
	MediaFileID          int    `json:"media_file_id"`
	FileSize             int64  `json:"file_size"`
	FileModifiedUnixNano int64  `json:"file_modified_unix_nano,omitempty"`
	FileHash             string `json:"file_hash,omitempty"`
	ProbeUpdatedUnixNano int64  `json:"probe_updated_unix_nano,omitempty"`
	VideoStreamIndex     int    `json:"video_stream_index"`
	StreamSignature      string `json:"stream_signature"`
}

// RevisionForFile captures catalog, filesystem, and primary-video facts that
// must remain unchanged while a tone-map recipe or preflight verdict is reused.
func RevisionForFile(file *models.MediaFile) SourceRevision {
	if file == nil {
		return SourceRevision{}
	}
	revision := SourceRevision{
		MediaFileID:      file.ID,
		FileSize:         file.FileSize,
		FileHash:         strings.TrimSpace(file.FileHash),
		VideoStreamIndex: 0,
	}
	if file.FileModifiedAt != nil {
		revision.FileModifiedUnixNano = normalizeRevisionTime(*file.FileModifiedAt).UnixNano()
	}
	if file.ProbeUpdatedAt != nil {
		revision.ProbeUpdatedUnixNano = normalizeRevisionTime(*file.ProbeUpdatedAt).UnixNano()
	}
	if len(file.VideoTracks) > 0 {
		revision.StreamSignature = StreamSignatureForTrack(file.VideoTracks[0])
	}
	return revision
}

// StreamSignatureForTrack returns the normalized primary-video identity frozen
// into a tone-map recipe.
func StreamSignatureForTrack(track models.VideoTrack) string {
	return hashRevisionValue(fmt.Sprintf(
		"%s|%s|%d|%d|%dx%d|%s|%d|%d|%t|%t|%t|%t|%t|%s|%s|%s|%s|%d|%s",
		track.Codec, track.Profile, track.Level, track.DVProfile, track.Width, track.Height,
		track.FrameRate, track.DVLevel, track.DVBLCompatID, track.DVConfigPresent,
		track.DVBLCompatIDPresent, track.DVBLPresent, track.DVELPresent, track.DVRPUPresent,
		track.ColorRange, track.ColorPrimaries, track.ColorTransfer, track.ColorSpace,
		track.BitDepth, track.PixelFormat,
	))
}

// ValidateLivePrimaryVideoTrack requires exact agreement between the frozen
// scanner-normalized stream signature and a freshly normalized live track.
func ValidateLivePrimaryVideoTrack(frozen SourceRevision, live models.VideoTrack) error {
	if frozen.StreamSignature == "" || StreamSignatureForTrack(live) != frozen.StreamSignature {
		return ErrSourceRevisionChanged
	}
	return nil
}

// Stable reports whether the revision contains every fact required for safe
// cross-request preflight caching.
func (r SourceRevision) Stable() bool {
	return r.MediaFileID > 0 && r.FileSize > 0 && r.FileModifiedUnixNano > 0 &&
		r.FileHash != "" && r.ProbeUpdatedUnixNano > 0 && r.StreamSignature != ""
}

// IsZero reports whether no source revision was frozen in the recipe.
func (r SourceRevision) IsZero() bool {
	return r.MediaFileID == 0 && r.FileSize == 0 && r.FileModifiedUnixNano == 0 &&
		r.FileHash == "" && r.ProbeUpdatedUnixNano == 0 && r.StreamSignature == ""
}

// Fingerprint returns the deterministic cache identity of all revision fields.
func (r SourceRevision) Fingerprint() string {
	data, _ := json.Marshal(r)
	return hashRevisionValue(string(data))
}

// Encode serializes a non-zero revision for transport in a transcode recipe.
func (r SourceRevision) Encode() string {
	if r.IsZero() {
		return ""
	}
	data, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeSourceRevision parses the URL-safe representation carried by a frozen
// transcode recipe; an empty value represents a zero revision.
func DecodeSourceRevision(value string) (SourceRevision, error) {
	if strings.TrimSpace(value) == "" {
		return SourceRevision{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return SourceRevision{}, fmt.Errorf("decode source revision: %w", err)
	}
	var revision SourceRevision
	if err := json.Unmarshal(data, &revision); err != nil {
		return SourceRevision{}, fmt.Errorf("decode source revision: %w", err)
	}
	return revision, nil
}

// ValidatePath rejects a frozen recipe when the source bytes visible to the
// executor no longer match the scanner revision. Current scanner hashes use the
// 16-character OpenSubtitles format and are rechecked with size and mtime; older
// catalog values still receive the filesystem checks they had before hashes were
// validated at execution time.
func (r SourceRevision) ValidatePath(path string) error {
	if r.IsZero() {
		return nil
	}
	if r.MediaFileID <= 0 {
		return ErrSourceRevisionChanged
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %w", ErrSourceRevisionChanged, err)
		}
		return fmt.Errorf("stat tone-map source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != r.FileSize {
		return ErrSourceRevisionChanged
	}
	if r.FileModifiedUnixNano > 0 && normalizeRevisionTime(info.ModTime()).UnixNano() != r.FileModifiedUnixNano {
		return ErrSourceRevisionChanged
	}
	if isOpenSubtitlesHash(r.FileHash) {
		currentHash, err := computeOpenSubtitlesHash(path, info.Size())
		if err != nil {
			return fmt.Errorf("hash tone-map source: %w", err)
		}
		if !strings.EqualFold(currentHash, strings.TrimSpace(r.FileHash)) {
			return ErrSourceRevisionChanged
		}
	}
	return nil
}

const openSubtitlesHashBlockSize = 64 * 1024

func isOpenSubtitlesHash(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 16 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func computeOpenSubtitlesHash(path string, size int64) (string, error) {
	if size < 2*openSubtitlesHashBlockSize {
		return "", fmt.Errorf("file is too small for OpenSubtitles hash")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	hash := uint64(size)
	buffer := make([]byte, openSubtitlesHashBlockSize)
	for _, offset := range []int64{0, size - openSubtitlesHashBlockSize} {
		if _, err := file.ReadAt(buffer, offset); err != nil {
			return "", err
		}
		for i := 0; i < len(buffer); i += 8 {
			hash += binary.LittleEndian.Uint64(buffer[i : i+8])
		}
	}
	return fmt.Sprintf("%016x", hash), nil
}

// normalizeRevisionTime matches the database's microsecond timestamp precision
// before filesystem times are frozen or compared.
func normalizeRevisionTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

// hashRevisionValue returns a fixed-size, non-sensitive identity for cache keys.
func hashRevisionValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
