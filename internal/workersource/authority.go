// Package workersource is the core-owned source authority for standalone workers.
package workersource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

var (
	ErrDenied              = errors.New("source access denied")
	ErrNotFound            = errors.New("source grant missing")
	ErrExpired             = errors.New("source grant expired")
	ErrChanged             = errors.New("source revision changed")
	ErrQuota               = errors.New("source quota exceeded")
	ErrInactive            = errors.New("source work inactive")
	ErrUnavailable         = errors.New("source authority unavailable")
	ErrIdentityUnavailable = errors.New("source file identity unavailable")
)

const DefaultTTL = 120 * time.Second
const RenewAfter = 45 * time.Second

type Limits struct {
	MaxGrants              int
	MaxSourceBytes         int64
	MaxReaders, ChunkBytes int
}
type Options struct {
	Store         Store
	Origin        string
	PathApprover  func(context.Context, string, string) error
	Active        func(context.Context, int, string, uint64) bool
	VerifyRequest func(*http.Request, int) error
	Limits        Limits
	Now           func() time.Time
}

// Record contains core-only metadata. Only Source may be sent to a worker.
type Record struct {
	Source    workerwire.Source `json:"source"`
	Path      string            `json:"path"`
	Identity  FileIdentity      `json:"identity"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// Store mutations must be atomic, including quota accounting and work revocation.
// Stores are core dependencies; standalone workers never construct them.
type Store interface {
	Create(context.Context, Record, Limits) error
	Get(context.Context, string) (Record, error)
	Extend(context.Context, string, time.Time) error
	Delete(context.Context, string) error
	RevokeWork(context.Context, int, string, uint64) error
}

type Authority struct {
	options     Options
	routePrefix string
	readers     chan struct{}
}

func New(o Options) (*Authority, error) {
	if o.Store == nil || o.PathApprover == nil || o.Active == nil || o.VerifyRequest == nil {
		return nil, ErrDenied
	}
	u, err := url.Parse(o.Origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, ErrDenied
	}
	o.Origin = strings.TrimRight(o.Origin, "/")
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Limits.MaxGrants == 0 {
		o.Limits.MaxGrants = 1024
	}
	if o.Limits.MaxSourceBytes == 0 {
		o.Limits.MaxSourceBytes = 1 << 40
	}
	if o.Limits.MaxReaders == 0 {
		o.Limits.MaxReaders = 32
	}
	if o.Limits.ChunkBytes == 0 {
		o.Limits.ChunkBytes = 64 << 10
	}
	if o.Limits.MaxGrants < 1 || o.Limits.MaxGrants > 65536 || o.Limits.MaxSourceBytes < 1 || o.Limits.MaxSourceBytes > 1<<52 || o.Limits.MaxReaders < 1 || o.Limits.MaxReaders > 4096 || o.Limits.ChunkBytes < 1 || o.Limits.ChunkBytes > 1<<20 {
		return nil, ErrQuota
	}
	return &Authority{options: o, routePrefix: strings.TrimRight(u.Path, "/") + "/api/internal/workers/", readers: make(chan struct{}, o.Limits.MaxReaders)}, nil
}

func validWork(node int, work string, generation uint64) bool {
	return node > 0 && work != "" && len(work) <= 256 && strings.IndexFunc(work, func(r rune) bool { return r < 32 }) < 0 && generation > 0
}
func validKind(kind string) bool {
	switch kind {
	case "media", "theme", "subtitle", "font", "attachment":
		return true
	}
	return false
}

// Acquire is called by admitted core work, never by a viewer-facing request.
func (a *Authority) Acquire(ctx context.Context, node int, work string, generation uint64, path, kind string) (workerwire.Source, error) {
	if err := ctx.Err(); err != nil {
		return workerwire.Source{}, err
	}
	if !validWork(node, work, generation) || !validKind(kind) || !filepath.IsAbs(path) {
		return workerwire.Source{}, ErrDenied
	}
	if !a.options.Active(ctx, node, work, generation) {
		return workerwire.Source{}, ErrInactive
	}
	if err := a.options.PathApprover(ctx, path, kind); err != nil {
		return workerwire.Source{}, ErrDenied
	}
	f, identity, err := openPin(path)
	if err != nil {
		return workerwire.Source{}, err
	}
	defer func() { _ = f.Close() }()
	if err := a.options.PathApprover(ctx, path, kind); err != nil {
		return workerwire.Source{}, ErrDenied
	}
	idBytes, _ := json.Marshal([]string{a.options.Origin, path, kind})
	id := sha256.Sum256(idBytes)
	ref := workerwire.SourceRef{ID: hex.EncodeToString(id[:]), Kind: kind}
	revisionBytes, _ := json.Marshal(struct {
		ID  string
		Pin FileIdentity
	}{ref.ID, identity})
	revision := sha256.Sum256(revisionBytes)
	ref.Revision = hex.EncodeToString(revision[:])
	grant := uuid.NewString()
	source := workerwire.Source{Ref: ref, Origin: a.options.Origin, URL: a.options.Origin + "/api/internal/workers/" + strconvNode(node) + "/sources/" + grant, Grant: grant, ETag: `"` + ref.Revision + `"`, Size: identity.Size, NodeID: node, WorkID: work, Generation: generation}
	record := Record{Source: source, Path: path, Identity: identity, ExpiresAt: a.options.Now().Add(DefaultTTL)}
	if err := validatePin(f, path, identity); err != nil {
		return workerwire.Source{}, err
	}
	if err := a.options.Store.Create(ctx, record, a.options.Limits); err != nil {
		return workerwire.Source{}, err
	}
	if !a.options.Active(ctx, node, work, generation) {
		_ = a.options.Store.Delete(ctx, grant)
		return workerwire.Source{}, ErrInactive
	}
	return source, nil
}

func (a *Authority) scoped(ctx context.Context, node int, work string, generation uint64, grant string) (Record, error) {
	r, err := a.options.Store.Get(ctx, grant)
	if err != nil {
		return Record{}, err
	}
	if r.Source.NodeID != node || r.Source.WorkID != work || r.Source.Generation != generation || r.Source.Origin != a.options.Origin {
		return Record{}, ErrDenied
	}
	if !a.options.Now().Before(r.ExpiresAt) {
		return Record{}, ErrExpired
	}
	return r, nil
}

func (a *Authority) Renew(ctx context.Context, node int, work string, generation uint64, grant string) (workerwire.Source, error) {
	if !a.options.Active(ctx, node, work, generation) {
		return workerwire.Source{}, ErrInactive
	}
	r, err := a.scoped(ctx, node, work, generation, grant)
	if err != nil {
		return workerwire.Source{}, err
	}
	if err := a.options.PathApprover(ctx, r.Path, r.Source.Ref.Kind); err != nil {
		return workerwire.Source{}, ErrDenied
	}
	f, identity, err := openPin(r.Path)
	if err != nil {
		return workerwire.Source{}, a.invalidate(ctx, grant, err)
	}
	defer func() { _ = f.Close() }()
	if identity != r.Identity {
		return workerwire.Source{}, a.invalidate(ctx, grant, ErrChanged)
	}
	if err := validatePin(f, r.Path, r.Identity); err != nil {
		return workerwire.Source{}, a.invalidate(ctx, grant, err)
	}
	if !a.options.Active(ctx, node, work, generation) {
		return workerwire.Source{}, ErrInactive
	}
	if err := a.options.Store.Extend(ctx, grant, a.options.Now().Add(DefaultTTL)); err != nil {
		return workerwire.Source{}, err
	}
	return r.Source, nil
}

func (a *Authority) Release(ctx context.Context, node int, work string, generation uint64, grant string) error {
	r, err := a.options.Store.Get(ctx, grant)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Source.NodeID != node || r.Source.WorkID != work || r.Source.Generation != generation {
		return ErrDenied
	}
	return a.options.Store.Delete(ctx, grant)
}
func (a *Authority) RevokeWork(ctx context.Context, node int, work string, generation uint64) error {
	if !validWork(node, work, generation) {
		return ErrDenied
	}
	return a.options.Store.RevokeWork(ctx, node, work, generation)
}
func (a *Authority) invalidate(ctx context.Context, grant string, cause error) error {
	if err := a.options.Store.Delete(ctx, grant); err != nil {
		return ErrUnavailable
	}
	return cause
}
func (a *Authority) Handler() http.Handler { return http.HandlerFunc(a.serveHTTP) }
