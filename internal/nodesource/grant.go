// Package nodesource gives transcode nodes their source media through the
// core, and only the media the core grants them.
//
// The core mints a source grant for every piece of node work: a stateless,
// signed capability to use one revision of one catalog file (or detail-page
// theme file) through one node (Grants). A node exchanges it at the core's
// open endpoint for the approved path and revision, renews it while the job
// runs (Opener), and reads that path from its own library mount.
//
// Grant paths are fspath-encoded catalog paths: every stat and open goes
// through internal/fspath.
package nodesource

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Kinds of source a grant names: a media_files row, or a detail-page theme
// file (item_theme_songs).
const (
	KindFile  = "file"
	KindTheme = "theme"
)

// GrantLifetime is how long a minted or renewed grant stays valid. A node
// renews well before that while its job runs; MaxGrantAge bounds how long it
// can go on renewing.
const GrantLifetime = time.Hour

// MaxGrantAge bounds a grant's whole life, from minting across every
// renewal: the core refuses to renew a grant, or to serve its bytes, once it
// is this old. A job that needs its source longer fails like any job whose
// renewal is refused.
//
// It is the longest any job can legitimately hold one source. A transcode
// session is reaped after ten idle minutes (a paused player) and its rebuild
// gets a fresh grant, so only continuous playback holds a grant, and a
// playback session cannot outlive its stream token's absolute life
// (playback.MaxTokenTTL, 24 hours). The slowest job is a download
// preparation that re-encodes a long film in software: a four-hour film at a
// quarter of real time takes 16 hours. 24 hours covers both, and bounds what
// a stolen node key and one captured grant can read to a day.
const MaxGrantAge = 24 * time.Hour

// RenewInterval is how often a node renews an open source's grant: a third of
// GrantLifetime, so renewal survives a core outage of about 40 minutes.
const RenewInterval = 20 * time.Minute

// grantVersion is the payload version a grant carries. Version 2 added the
// issue time (iat).
const grantVersion = 2

// legacyGrantVersion is the payload without an issue time, which builds
// between the two transcode node changes minted. One still held by a running
// job is read as the current version, its whole life counted from one
// lifetime before its expiry, so an update does not end that job.
const legacyGrantVersion = 1

// grantKeyLabel is the HKDF info string of the grant MAC key. Changing it, or
// reusing it for another purpose, breaks the key separation from every other
// key derived from the same secret.
const grantKeyLabel = "silo/node-source-grant/v1"

// maxGrantBytes bounds an encoded grant. A grant names one path, so anything
// near this is not one.
const maxGrantBytes = 8 << 10

var (
	// ErrInvalidGrant is a grant that does not verify: malformed, signed with
	// another key, of an unknown version, or naming nothing usable.
	ErrInvalidGrant = errors.New("nodesource: invalid source grant")
	// ErrGrantExpired is a grant past its expiry, or older than MaxGrantAge.
	ErrGrantExpired = errors.New("nodesource: source grant expired")
	// ErrNoGrantKey is a core without the secret grants are signed with.
	ErrNoGrantKey = errors.New("nodesource: no source grant key is configured")
	// ErrSourceGone is a granted source that no longer exists as a regular
	// file, or that the catalog no longer has.
	ErrSourceGone = errors.New("nodesource: source is gone")
	// ErrSourceChanged is a granted source whose size or modification time
	// differs from its grant's revision.
	ErrSourceChanged = errors.New("nodesource: source changed")
)

// Grant is a source grant's payload. The node can read it: with local source
// access it is given the path anyway.
type Grant struct {
	V    int    `json:"v"`
	Node int    `json:"node"`
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
	// Path is the exact, fspath-encoded catalog path.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// ModTimeUnixNano is the modification time the core's stat returned, in
	// Unix nanoseconds. Revisions compare it at microseconds (SameModTime).
	ModTimeUnixNano int64 `json:"mtime_ns"`
	// Iat is when the grant was first minted, in Unix seconds. Renewal keeps
	// it, so it bounds the grant's whole life (MaxGrantAge).
	Iat int64 `json:"iat"`
	Exp int64 `json:"exp"`
}

// Revision identifies one version of a source independently of where it is
// mounted: kind, id, size and modification time, leaving out device, inode
// and change time. The modification time is kept as the core's stat returned
// it, in nanoseconds, so a caller can fill a nanosecond revision from it (for
// example playback.SubtitleRenditionSource); revisions compare it at
// microseconds, the catalog's precision (models.NormalizeFileModifiedAt), as
// tone-map revisions do across hosts.
type Revision struct {
	Kind            string
	ID              int64
	Size            int64
	ModTimeUnixNano int64
}

// SameModTime reports whether two modification times are equal at revision
// precision: microseconds, as the catalog stores them.
func SameModTime(a, b time.Time) bool {
	return models.NormalizeFileModifiedAt(a).Equal(models.NormalizeFileModifiedAt(b))
}

// ModTime is the revision's modification time.
func (r Revision) ModTime() time.Time { return time.Unix(0, r.ModTimeUnixNano) }

// Same reports whether r and other name the same revision at revision
// precision.
func (r Revision) Same(other Revision) bool {
	return r.Kind == other.Kind && r.ID == other.ID && r.Size == other.Size && SameModTime(r.ModTime(), other.ModTime())
}

// ETag is the revision's protocol entity tag,
// `"<kind>-<id>-<size>-<mtime in Unix microseconds>"`: it carries the
// modification time at the precision revisions compare at.
func (r Revision) ETag() string {
	return fmt.Sprintf(`"%s-%d-%d-%d"`, r.Kind, r.ID, r.Size, models.NormalizeFileModifiedAt(r.ModTime()).UnixMicro())
}

// Revision is the source revision the grant pins.
func (g Grant) Revision() Revision {
	return Revision{Kind: g.Kind, ID: g.ID, Size: g.Size, ModTimeUnixNano: g.ModTimeUnixNano}
}

// ETag is the protocol entity tag of the grant's revision.
func (g Grant) ETag() string { return g.Revision().ETag() }

// Expires is when the grant stops being valid: its expiry, or the end of its
// whole life (MaxGrantAge) when that comes first.
func (g Grant) Expires() time.Time {
	return time.Unix(min(g.Exp, g.Iat+int64(MaxGrantAge/time.Second)), 0)
}

// IssuedAt is when the grant was first minted.
func (g Grant) IssuedAt() time.Time { return time.Unix(g.Iat, 0) }

// Matches reports whether info describes the grant's revision: a regular file
// of its size and modification time, to the microsecond.
func (g Grant) Matches(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() == g.Size && SameModTime(info.ModTime(), g.Revision().ModTime())
}

// SourceError classifies a failed stat or open of a source path. The path
// no longer naming a file (it is missing, or a parent stopped being a
// directory) is ErrSourceGone. Any other failure, such as an I/O error or a
// stale or disconnected network mount, says nothing about the file: it is
// returned as it is, so a job reading an unchanged file is not ended over it.
func SourceError(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return fmt.Errorf("%w: %w", ErrSourceGone, err)
	}
	return err
}

// statSource stats path through fspath, following symlinks as the scanner
// does for catalog paths. It returns ErrSourceGone for a missing or
// non-regular file, and the stat's own error when the file could not be
// examined.
func statSource(ctx context.Context, path string) (os.FileInfo, error) {
	info, err := fspath.StatContext(ctx, path)
	if err != nil {
		return nil, SourceError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", ErrSourceGone)
	}
	return info, nil
}

// Check stats the grant's path. It returns ErrSourceGone for a missing or
// non-regular file, ErrSourceChanged for one of another size or modification
// time, and the stat's own error when the file could not be examined.
func (g Grant) Check(ctx context.Context) (os.FileInfo, error) {
	info, err := statSource(ctx, g.Path)
	if err != nil {
		return nil, err
	}
	if !g.Matches(info) {
		return nil, ErrSourceChanged
	}
	return info, nil
}

// Grants mints and verifies source grants. Only the core holds the key: it
// is derived from the secret stream tokens are signed with.
type Grants struct {
	secret   func() string
	now      func() time.Time
	lifetime time.Duration
}

// NewGrants returns the grants keyed by the secret secret returns, read on
// every use so a rotated secret applies at once.
func NewGrants(secret func() string) *Grants {
	return NewGrantsWithClock(secret, time.Now)
}

// NewGrantsWithClock is NewGrants reading the time from now, so a test can
// age grants past their expiry or MaxGrantAge.
func NewGrantsWithClock(secret func() string, now func() time.Time) *Grants {
	return &Grants{secret: secret, now: now, lifetime: GrantLifetime}
}

// ForFile mints a grant for node nodeID to use media file file, at the
// revision one stat on this host finds now. It reads no catalog: the caller
// holds the row.
func (g *Grants) ForFile(ctx context.Context, nodeID int, file *models.MediaFile) (string, error) {
	if file == nil {
		return "", fmt.Errorf("%w: no media file", ErrInvalidGrant)
	}
	return g.mint(ctx, nodeID, KindFile, int64(file.ID), file.FilePath)
}

// ForTheme mints a grant for node nodeID to use theme file themeID at path,
// at the revision one stat on this host finds now.
func (g *Grants) ForTheme(ctx context.Context, nodeID int, themeID int64, path string) (string, error) {
	return g.mint(ctx, nodeID, KindTheme, themeID, path)
}

func (g *Grants) mint(ctx context.Context, nodeID int, kind string, id int64, path string) (string, error) {
	claims := Grant{V: grantVersion, Node: nodeID, Kind: kind, ID: id, Path: path}
	if !claims.wellFormed() {
		return "", fmt.Errorf("%w: node %d, %s %d", ErrInvalidGrant, nodeID, kind, id)
	}
	// Fail before the stat when no grant can be signed anyway.
	if _, err := g.key(); err != nil {
		return "", err
	}
	info, err := statSource(ctx, path)
	if err != nil {
		return "", err
	}
	claims.Size, claims.ModTimeUnixNano = info.Size(), info.ModTime().UnixNano()
	claims.Iat = g.now().Unix()
	return g.sign(claims)
}

// Renew returns claims as a grant valid for another lifetime from now, but
// never past MaxGrantAge after it was first minted. It keeps the revision and
// the issue time: renewal never moves a job to another version of its source,
// and never extends a grant's whole life.
func (g *Grants) Renew(claims Grant) (string, error) {
	if !g.now().Before(claims.IssuedAt().Add(MaxGrantAge)) {
		return "", ErrGrantExpired
	}
	return g.sign(claims)
}

func (g *Grants) sign(claims Grant) (string, error) {
	key, err := g.key()
	if err != nil {
		return "", err
	}
	claims.Exp = min(g.now().Add(g.lifetime).Unix(), claims.IssuedAt().Add(MaxGrantAge).Unix())
	payload, _ := json.Marshal(claims) // strings and integers only
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(grantMAC(key, payload))
	// Verify refuses anything longer, so never hand out a grant that could
	// not be presented back.
	if len(token) > maxGrantBytes {
		return "", fmt.Errorf("%w: grant of %d bytes exceeds %d", ErrInvalidGrant, len(token), maxGrantBytes)
	}
	return token, nil
}

// Verify checks token's MAC, expiry and whole life (MaxGrantAge) and returns
// its claims. A token that does not verify is ErrInvalidGrant; one that has
// expired or is too old, ErrGrantExpired.
func (g *Grants) Verify(token string) (Grant, error) {
	key, err := g.key()
	if err != nil {
		return Grant{}, err
	}
	payload, mac, err := splitGrant(token)
	if err != nil {
		return Grant{}, err
	}
	if !hmac.Equal(mac, grantMAC(key, payload)) {
		return Grant{}, ErrInvalidGrant
	}
	claims, err := decodeGrant(payload)
	if err != nil {
		return Grant{}, err
	}
	if !g.now().Before(claims.Expires()) {
		return Grant{}, ErrGrantExpired
	}
	return claims, nil
}

func (g *Grants) key() ([]byte, error) {
	if g == nil || g.secret == nil {
		return nil, ErrNoGrantKey
	}
	secret := g.secret()
	if strings.TrimSpace(secret) == "" {
		return nil, ErrNoGrantKey
	}
	return hkdf.Key(sha256.New, []byte(secret), nil, grantKeyLabel, sha256.Size)
}

func grantMAC(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

// GrantRevision is the source revision token pins, read without verifying
// it. The core compares the grants it minted itself this way; nothing may
// trust it for a grant it was handed.
func GrantRevision(token string) (Revision, error) {
	claims, err := peek(token)
	if err != nil {
		return Revision{}, err
	}
	return claims.Revision(), nil
}

// peek returns token's claims without verifying them. A node reads its own
// grant's expiry this way; nothing may trust what peek returns.
func peek(token string) (Grant, error) {
	payload, _, err := splitGrant(token)
	if err != nil {
		return Grant{}, err
	}
	return decodeGrant(payload)
}

func splitGrant(token string) (payload, mac []byte, err error) {
	if len(token) > maxGrantBytes {
		return nil, nil, ErrInvalidGrant
	}
	encodedPayload, encodedMAC, ok := strings.Cut(token, ".")
	if !ok || encodedPayload == "" || encodedMAC == "" {
		return nil, nil, ErrInvalidGrant
	}
	payload, err = base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return nil, nil, ErrInvalidGrant
	}
	mac, err = base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil || len(mac) != sha256.Size {
		return nil, nil, ErrInvalidGrant
	}
	return payload, mac, nil
}

func decodeGrant(payload []byte) (Grant, error) {
	var claims Grant
	if err := json.Unmarshal(payload, &claims); err != nil || !claims.wellFormed() || claims.Size < 0 || claims.Exp <= 0 {
		return Grant{}, ErrInvalidGrant
	}
	switch {
	case claims.V == grantVersion && claims.Iat > 0:
	case claims.V == legacyGrantVersion && claims.Iat == 0:
		claims.V = grantVersion
		claims.Iat = claims.Exp - int64(GrantLifetime/time.Second)
	default:
		return Grant{}, ErrInvalidGrant
	}
	return claims, nil
}

// wellFormed reports whether the claims name a node and a source a grant can
// be for: a positive node and id, a known kind, and a plain absolute path.
// The path must be valid UTF-8, as an fspath-encoded path always is: JSON
// would silently replace any other byte and the grant would name another
// file.
func (g Grant) wellFormed() bool {
	return g.Node > 0 && g.ID > 0 && (g.Kind == KindFile || g.Kind == KindTheme) && plainAbsolutePath(g.Path)
}

func plainAbsolutePath(path string) bool {
	return path != "" && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path)
}

// FileGranter mints the grant a transcode node uses a media file with.
// *Grants implements it.
type FileGranter interface {
	ForFile(ctx context.Context, nodeID int, file *models.MediaFile) (string, error)
}

// GrantFile mints granter's grant for node nodeID to use file. Every piece of
// work a core sends a transcode node carries one; a failure fails the
// dispatch to that node. A nil granter (a core wired without source grants,
// as some tests are) mints nothing.
func GrantFile(ctx context.Context, granter FileGranter, nodeID int, file *models.MediaFile) (string, error) {
	if granter == nil {
		return "", nil
	}
	if grants, ok := granter.(*Grants); ok && grants == nil {
		return "", nil
	}
	return granter.ForFile(ctx, nodeID, file)
}
