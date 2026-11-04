package overlays

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// Image library limits. Badges draw an image a few dozen pixels tall, so a
// stored image never needs more than 600x300; the upload cap leaves room for
// an uncompressed source that re-encodes far smaller.
const (
	MaxImageUploadBytes = 4 << 20
	MaxImages           = 256
	MaxImageNameLength  = 64

	maxImageWidth  = 600
	maxImageHeight = 300

	imageKeyPrefix = "overlay-images/"
)

var (
	ErrImageNotFound           = errors.New("overlays: image not found")
	ErrUnsupportedImage        = errors.New("overlays: unsupported image")
	ErrImageTooLarge           = errors.New("overlays: image exceeds the upload limit")
	ErrInvalidImageName        = errors.New("overlays: invalid image name")
	ErrImageLibraryFull        = errors.New("overlays: image library is full")
	ErrImageStorageUnavailable = errors.New("overlays: image storage is not configured")
)

// uploadTypes are the media types Upload accepts; every one re-encodes to WebP.
var uploadTypes = map[string]bool{mediaTypePNG: true, "image/jpeg": true, "image/webp": true}

const mediaTypePNG = "image/png"

// imageRefPattern matches what Upload mints; card-overlays.json's imageRef
// carries the same pattern.
var imageRefPattern = regexp.MustCompile(`^[0-9a-f]{64}\.webp$`)

// IsImageRef reports whether ref has the shape of an uploaded image's ref.
func IsImageRef(ref string) bool { return imageRefPattern.MatchString(ref) }

// Image is one uploaded badge image.
type Image struct {
	Ref       string
	Name      string
	Width     int
	Height    int
	Bytes     int
	CreatedAt time.Time
}

// ImageDB is the slice of *pgxpool.Pool the image library uses.
type ImageDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ImageStore keeps image bytes in the assets blob store.
type ImageStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error)
	Delete(ctx context.Context, keys []string) (int, error)
}

// ImageLibrary stores the images administrators upload for overlay badges.
// Images are content-addressed: the ref is the hash of the stored bytes, so a
// URL that names a ref always serves the same image and clients cache it for
// good. A row in overlay_images is what makes a ref current.
type ImageLibrary struct {
	db    ImageDB
	store ImageStore
}

// NewImageLibrary returns the library. A nil store leaves uploads and serving
// unavailable while listing still works.
func NewImageLibrary(db ImageDB, store ImageStore) *ImageLibrary {
	return &ImageLibrary{db: db, store: store}
}

// HasStorage reports whether images can be uploaded and served.
func (l *ImageLibrary) HasStorage() bool { return l != nil && l.store != nil }

func imageKey(ref string) string { return imageKeyPrefix + ref }

// Upload re-encodes an uploaded image to WebP, stores it, and records it
// under name. Uploading bytes that re-encode to an image already in the
// library keeps that one row and renames it.
func (l *ImageLibrary) Upload(ctx context.Context, name, declaredType string, data []byte) (Image, error) {
	if !l.HasStorage() {
		return Image{}, ErrImageStorageUnavailable
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxImageNameLength {
		return Image{}, ErrInvalidImageName
	}
	if !uploadTypes[declaredType] {
		return Image{}, ErrUnsupportedImage
	}
	if len(data) > MaxImageUploadBytes {
		return Image{}, ErrImageTooLarge
	}
	out, width, height, err := imageutil.EncodeWebPWithin(data, maxImageWidth, maxImageHeight)
	if err != nil {
		if errors.Is(err, imageutil.ErrInvalidImage) || imageutil.PixelDataUndecodable(data) {
			return Image{}, ErrUnsupportedImage
		}
		return Image{}, fmt.Errorf("encoding overlay image: %w", err)
	}
	sum := sha256.Sum256(out)
	ref := hex.EncodeToString(sum[:]) + ".webp"

	var exists bool
	var count int
	if err := l.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM overlay_images WHERE ref = $1), (SELECT count(*) FROM overlay_images)`, ref).Scan(&exists, &count); err != nil {
		return Image{}, fmt.Errorf("checking overlay image library: %w", err)
	}
	// Two uploads racing past this check can take the library a little over
	// the cap; it bounds an administrator's library, not an attack.
	if !exists && count >= MaxImages {
		return Image{}, ErrImageLibraryFull
	}
	if err := l.store.Put(ctx, imageKey(ref), out); err != nil {
		return Image{}, fmt.Errorf("storing overlay image: %w", err)
	}
	img := Image{Ref: ref, Name: name, Width: width, Height: height, Bytes: len(out)}
	if err := l.db.QueryRow(ctx, `
		INSERT INTO overlay_images (ref, name, width, height, bytes)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (ref) DO UPDATE SET name = EXCLUDED.name
		RETURNING created_at`, ref, name, width, height, len(out)).Scan(&img.CreatedAt); err != nil {
		// Bytes no row names would never be served or deleted. An image that
		// was already in the library keeps its bytes.
		if !exists {
			if _, delErr := l.store.Delete(context.WithoutCancel(ctx), []string{imageKey(ref)}); delErr != nil {
				slog.WarnContext(ctx, "removing unrecorded overlay image bytes failed", "component", "overlays", "ref", ref, "error", delErr)
			}
		}
		return Image{}, fmt.Errorf("recording overlay image: %w", err)
	}
	return img, nil
}

// List returns every image in upload order.
func (l *ImageLibrary) List(ctx context.Context) ([]Image, error) {
	rows, err := l.db.Query(ctx, `SELECT ref, name, width, height, bytes, created_at FROM overlay_images ORDER BY created_at, ref`)
	if err != nil {
		return nil, fmt.Errorf("listing overlay images: %w", err)
	}
	defer rows.Close()
	images := []Image{}
	for rows.Next() {
		var img Image
		if err := rows.Scan(&img.Ref, &img.Name, &img.Width, &img.Height, &img.Bytes, &img.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning overlay image: %w", err)
		}
		images = append(images, img)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing overlay images: %w", err)
	}
	return images, nil
}

// Delete removes an image from the library and its bytes from storage.
// Documents that still name the ref fall back to the badge's own rendering.
func (l *ImageLibrary) Delete(ctx context.Context, ref string) error {
	if !IsImageRef(ref) {
		return ErrImageNotFound
	}
	tag, err := l.db.Exec(ctx, `DELETE FROM overlay_images WHERE ref = $1`, ref)
	if err != nil {
		return fmt.Errorf("deleting overlay image: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrImageNotFound
	}
	// The row is what makes an image current, so it is already gone; bytes a
	// failed delete leaves behind are unreachable, not served.
	if l.store != nil {
		if _, err := l.store.Delete(ctx, []string{imageKey(ref)}); err != nil {
			slog.WarnContext(ctx, "deleting overlay image bytes failed", "component", "overlays", "ref", ref, "error", err)
		}
	}
	return nil
}

// Get returns the stored bytes of a current image.
func (l *ImageLibrary) Get(ctx context.Context, ref string) ([]byte, error) {
	if !IsImageRef(ref) {
		return nil, ErrImageNotFound
	}
	if !l.HasStorage() {
		return nil, ErrImageStorageUnavailable
	}
	var exists bool
	if err := l.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM overlay_images WHERE ref = $1)`, ref).Scan(&exists); err != nil {
		return nil, fmt.Errorf("reading overlay image: %w", err)
	}
	if !exists {
		return nil, ErrImageNotFound
	}
	reader, _, err := l.store.Get(ctx, imageKey(ref))
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return nil, ErrImageNotFound
		}
		return nil, fmt.Errorf("reading overlay image bytes: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxImageUploadBytes+1))
	_ = reader.Close()
	if err != nil {
		return nil, fmt.Errorf("reading overlay image bytes: %w", err)
	}
	return data, nil
}
