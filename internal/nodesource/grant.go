// Package nodesource gives transcode nodes their source media through the
// core, and only the media the core grants them.
//
// The core mints a source grant for every piece of node work: a stateless,
// signed capability to use one revision of one catalog file through one
// node (Grants). A node exchanges it at the core's open endpoint for the
// approved path and metadata, renews it while the job runs, and then either
// reads that path from its own mount (local source access) or reads the
// bytes over HTTP from the core through a loopback relay FFmpeg can seek in
// (core source access, Relay).
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Kinds of source a grant names: a media_files row, or a detail-page theme
// file (item_theme_songs).
const (
	KindFile  = "file"
	KindTheme = "theme"
)

// GrantLifetime is how long a minted or renewed grant stays valid. A node
// renews well before that while its job runs, so a job has no lifetime limit.
const GrantLifetime = time.Hour

// grantVersion is the payload version a grant carries.
const grantVersion = 1

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
	// ErrGrantExpired is a grant past its expiry.
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

// Grant is a source grant's payload. The node can read it: in local source
// access it is given the path anyway.
type Grant struct {
	V    int    `json:"v"`
	Node int    `json:"node"`
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
	// Path is the exact catalog path, so serving the bytes needs no catalog
	// lookup.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Mtime is the modification time in whole Unix seconds (see Revision).
	Mtime int64 `json:"mtime"`
	Exp   int64 `json:"exp"`
}

// Revision identifies one version of a source independently of where it is
// mounted: every API replica computes the same revision for a shared library,
// so it deliberately leaves out the device, inode and change time. The
// modification time is truncated to whole seconds (RevisionMtime), the
// precision every mount reports: sshfs keeps seconds, SMB 100 ns, and FUSE
// file systems vary, so a node or replica reading the library through
// another mount still sees the same revision.
type Revision struct {
	Kind  string
	ID    int64
	Size  int64
	Mtime int64
}

// RevisionMtime is a modification time as a revision carries it: whole Unix
// seconds.
func RevisionMtime(modTime time.Time) int64 { return modTime.Unix() }

// sameRevision reports whether info is a regular file of size bytes modified
// at mtime, at revision precision.
func sameRevision(info os.FileInfo, size, mtime int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() == size && RevisionMtime(info.ModTime()) == mtime
}

// Revision is the source revision the grant pins.
func (g Grant) Revision() Revision {
	return Revision{Kind: g.Kind, ID: g.ID, Size: g.Size, Mtime: g.Mtime}
}

// ETag is the protocol entity tag of the grant's revision.
func (g Grant) ETag() string { return g.Revision().ETag() }

// ETag is the revision's protocol entity tag, `"<kind>-<id>-<size>-<mtime>"`.
func (r Revision) ETag() string {
	return fmt.Sprintf(`"%s-%d-%d-%d"`, r.Kind, r.ID, r.Size, r.Mtime)
}

// Expires is when the grant stops being valid.
func (g Grant) Expires() time.Time { return time.Unix(g.Exp, 0) }

// Matches reports whether info describes the grant's revision: a regular file
// of its size and modification time, to the second.
func (g Grant) Matches(info os.FileInfo) bool {
	return sameRevision(info, g.Size, g.Mtime)
}

// Check stats the grant's path, following symlinks as the scanner does for
// catalog paths. It returns ErrSourceGone for a missing or non-regular file
// and ErrSourceChanged for one of another size or modification time.
func (g Grant) Check() (os.FileInfo, error) {
	info, err := os.Stat(g.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceGone, err)
	}
	return info, g.check(info)
}

func (g Grant) check(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", ErrSourceGone)
	}
	if !g.Matches(info) {
		return ErrSourceChanged
	}
	return nil
}

// Open opens the grant's path for reading and checks the open descriptor
// against the grant, so whatever is served from it is the granted revision
// even if the path is replaced right after. The open does not block on a FIFO
// raced into the path's place. Errors are those of Check.
func (g Grant) Open() (*os.File, os.FileInfo, error) {
	f, err := openNonblocking(g.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrSourceGone, err)
	}
	info, err := f.Stat()
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrSourceGone, err)
	} else {
		err = g.check(info)
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, info, nil
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
	return &Grants{secret: secret, now: time.Now, lifetime: GrantLifetime}
}

// ForFile mints a grant for node nodeID to use media file file, at the
// revision a stat on this host finds now.
func (g *Grants) ForFile(_ context.Context, nodeID int, file *models.MediaFile) (string, error) {
	if file == nil {
		return "", fmt.Errorf("%w: no media file", ErrInvalidGrant)
	}
	return g.mint(nodeID, KindFile, int64(file.ID), file.FilePath)
}

// ForTheme mints a grant for node nodeID to use theme file themeID at path,
// at the revision a stat on this host finds now.
func (g *Grants) ForTheme(_ context.Context, nodeID int, themeID int64, path string) (string, error) {
	return g.mint(nodeID, KindTheme, themeID, path)
}

func (g *Grants) mint(nodeID int, kind string, id int64, path string) (string, error) {
	claims := Grant{V: grantVersion, Node: nodeID, Kind: kind, ID: id, Path: path}
	if !claims.wellFormed() {
		return "", fmt.Errorf("%w: node %d, %s %d", ErrInvalidGrant, nodeID, kind, id)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSourceGone, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: not a regular file", ErrSourceGone)
	}
	claims.Size, claims.Mtime = info.Size(), RevisionMtime(info.ModTime())
	return g.sign(claims)
}

// Renew returns claims as a grant valid for another lifetime from now. It
// keeps the revision: renewal never moves a job to another version of its
// source.
func (g *Grants) Renew(claims Grant) (string, error) {
	return g.sign(claims)
}

func (g *Grants) sign(claims Grant) (string, error) {
	key, err := g.key()
	if err != nil {
		return "", err
	}
	claims.Exp = g.now().Add(g.lifetime).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(grantMAC(key, payload))
	// Verify refuses anything longer, so never hand out a grant that could
	// not be presented back.
	if len(token) > maxGrantBytes {
		return "", fmt.Errorf("%w: grant of %d bytes exceeds %d", ErrInvalidGrant, len(token), maxGrantBytes)
	}
	return token, nil
}

// Verify checks token's MAC and expiry and returns its claims. A token that
// does not verify is ErrInvalidGrant; one that has expired, ErrGrantExpired.
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
	if secret == "" {
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

// peek returns token's claims without verifying it. A node reads its own
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
	if err := json.Unmarshal(payload, &claims); err != nil || claims.V != grantVersion || !claims.wellFormed() ||
		claims.Size < 0 || claims.Exp <= 0 {
		return Grant{}, ErrInvalidGrant
	}
	return claims, nil
}

// wellFormed reports whether the claims name a node and a source a grant can
// be for: a positive node and id, a known kind, and a plain absolute path.
func (g Grant) wellFormed() bool {
	return g.Node > 0 && g.ID > 0 && (g.Kind == KindFile || g.Kind == KindTheme) &&
		g.Path != "" && !strings.ContainsRune(g.Path, 0) && filepath.IsAbs(g.Path)
}

// FileGranter mints the grant a transcode node uses a media file with.
// *Grants implements it.
type FileGranter interface {
	ForFile(ctx context.Context, nodeID int, file *models.MediaFile) (string, error)
}

// GrantFile mints granter's grant for node nodeID to use file. Every piece of
// work a core sends a transcode node carries one, whatever the node's source
// access; a failure fails the dispatch. A nil granter (a core wired without
// source grants, as some tests are) mints nothing: the node then refuses the
// work, since it never trusts a bare input path.
func GrantFile(ctx context.Context, granter FileGranter, nodeID int, file *models.MediaFile) (string, error) {
	if granter == nil {
		return "", nil
	}
	if grants, ok := granter.(*Grants); ok && grants == nil {
		return "", nil
	}
	return granter.ForFile(ctx, nodeID, file)
}
