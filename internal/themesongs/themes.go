// Package themesongs discovers and serves user-managed detail-page audio.
package themesongs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrNotFound    = errors.New("theme not found")
	ErrUnavailable = errors.New("theme audio is unavailable on this node")
	ErrGrant       = errors.New("invalid theme playback grant")
)

const containerMP3 = "mp3"

const GrantLifetime = 5 * time.Minute

// OwnerDirectory recognizes local theme filenames without inspecting the disk.
// It is also used for removal events, after the audio file has disappeared.
func OwnerDirectory(path string) (string, bool) {
	if Container(path) == "" {
		return "", false
	}
	dir := filepath.Dir(path)
	if strings.EqualFold(filepath.Base(dir), "theme-music") {
		return filepath.Dir(dir), true
	}
	if strings.EqualFold(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), "theme") {
		return dir, true
	}
	return "", false
}

type Song struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	DurationSeconds int    `json:"duration_seconds"`
	Container       string `json:"container"`
}

type Set struct {
	OwnerID string `json:"owner_id"`
	Items   []Song `json:"items"`
}

// File is private catalog state. Paths never appear in a client document or grant.
type File struct {
	Song
	AudioCodec    string
	AudioChannels int
	BitrateKbps   int
	SampleRate    int
	FolderID      int
	OwnerPath     string
	OwnerType     string // Resolved catalog type; not persisted with the file probe.
	Path          string
	Size          int64
	Modified      time.Time
}

type Store interface {
	Resolve(context.Context, string, bool, catalog.AccessFilter) (string, []File, error)
}

type Identity struct {
	UserID         int    `json:"user_id"`
	ProfileID      string `json:"profile_id"`
	SessionID      string `json:"session_id"`
	PolicyRevision int64  `json:"policy_revision"`
}

type Grant struct {
	Identity
	OwnerID  string `json:"owner_id"`
	ThemeID  string `json:"theme_id"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	jwt.RegisteredClaims
}

type Service struct {
	Store Store
	key   []byte
}

func NewService(store Store, secret string) *Service {
	var key []byte
	if secret != "" {
		digest := sha256.Sum256([]byte("silo.theme-audio.v2\x00" + secret))
		key = digest[:]
	}
	return &Service{Store: store, key: key}
}

func (s *Service) Discover(ctx context.Context, id string, inherit bool, filter catalog.AccessFilter) (Set, error) {
	owner, files, err := s.Store.Resolve(ctx, id, inherit, filter)
	set := Set{OwnerID: owner, Items: []Song{}}
	for _, file := range files {
		set.Items = append(set.Items, file.Song)
	}
	return set, err
}

func (s *Service) Select(ctx context.Context, owner, id string, filter catalog.AccessFilter) (File, error) {
	resolved, files, err := s.Store.Resolve(ctx, owner, false, filter)
	if err != nil {
		return File{}, err
	}
	if resolved == owner {
		for _, file := range files {
			if file.ID == id {
				return file, nil
			}
		}
	}
	return File{}, ErrNotFound
}

func (s *Service) Mint(ctx context.Context, identity Identity, owner, id string, filter catalog.AccessFilter, accessExpiry time.Time) (string, time.Time, error) {
	if len(s.key) == 0 || identity.UserID <= 0 || identity.ProfileID == "" || identity.SessionID == "" {
		return "", time.Time{}, ErrGrant
	}
	file, err := s.Select(ctx, owner, id, filter)
	if err != nil {
		return "", time.Time{}, err
	}
	f, err := Open(file)
	if err != nil {
		return "", time.Time{}, err
	}
	_ = f.Close()
	now := time.Now()
	expires := now.Add(GrantLifetime)
	if accessExpiry.Before(expires) {
		expires = accessExpiry
	}
	if !expires.After(now) {
		return "", time.Time{}, ErrGrant
	}
	grant := Grant{Identity: identity, OwnerID: owner, ThemeID: id, Size: file.Size, Modified: file.Modified.UnixNano(), RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings{"theme-audio"}, ExpiresAt: jwt.NewNumericDate(expires), IssuedAt: jwt.NewNumericDate(now)}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, grant).SignedString(s.key)
	return token, expires, err
}

func (s *Service) Validate(token, owner, id string) (*Grant, error) {
	if len(s.key) == 0 || token == "" {
		return nil, ErrGrant
	}
	var grant Grant
	parsed, err := jwt.ParseWithClaims(token, &grant, func(_ *jwt.Token) (any, error) { return s.key, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience("theme-audio"), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid || grant.OwnerID != owner || grant.ThemeID != id || grant.UserID <= 0 || grant.ProfileID == "" || grant.SessionID == "" {
		return nil, ErrGrant
	}
	return &grant, nil
}

// Open confines delivery to the discovered directory, including symlink races,
// and refuses a replacement file until a scan and a new grant describe it.
func Open(file File) (*os.File, error) {
	rel, err := filepath.Rel(file.OwnerPath, file.Path)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, ErrUnavailable
	}
	root, err := os.OpenRoot(file.OwnerPath)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(rel)
	if err != nil {
		return nil, ErrUnavailable
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size || !info.ModTime().Truncate(time.Microsecond).Equal(file.Modified) {
		_ = f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

func Serve(w http.ResponseWriter, r *http.Request, file File, f *os.File) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", ContentType(file.Container))
	w.Header().Set("ETag", fmt.Sprintf(`"theme-%s-%d-%d"`, file.ID, file.Size, file.Modified.UnixNano()))
	http.ServeContent(httpstream.NewRollingDeadlineWriter(w), r, file.Title, file.Modified, f)
}

func ContentType(container string) string {
	return playback.MimeFromExtension("theme." + container)
}

func Container(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case containerMP3, "m4a", "m4b", "flac", "ogg", "opus", "wav", "aac":
		return ext
	default:
		return ""
	}
}

func NumericID(id string) (int64, bool) {
	n, err := strconv.ParseInt(id, 10, 64)
	return n, err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}
