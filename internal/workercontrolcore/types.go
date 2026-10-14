// Package workercontrolcore owns scoped standalone-worker authority on the core.
package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

var ErrFenced = errors.New("worker assignment fenced")
var ErrUnavailable = errors.New("worker authority unavailable")
var ErrDenied = errors.New("worker authority denied")

// ErrOutputRevoked is a definitive customer-credential failure. Ambiguous
// content/store failures must not use it: a later healthy snapshot can recover.
var ErrOutputRevoked = errors.New("worker output credential revoked")

type Node struct {
	ID        int
	URL       string
	PublicKey ed25519.PublicKey
	Enabled   bool
	AllowHTTP bool
}
type NodeRegistry interface {
	ByURL(context.Context, string) (Node, bool, error)
	ByID(context.Context, int) (Node, bool, error)
}
type AssignmentStore interface {
	Begin(context.Context, workerwire.Assignment) (workerwire.Assignment, error)
	Complete(context.Context, workerwire.Assignment) error
	Activate(context.Context, int, string, uint64) error
	Candidate(context.Context, int, string) (workerwire.Assignment, bool, error)
	Pending(context.Context, int) (map[string]uint64, error)
	Snapshot(context.Context, int) (AuthoritySnapshot, error)
	Get(context.Context, int, string) (workerwire.Assignment, bool, error)
	Active(context.Context, int) (map[string]uint64, error)
	IsActive(context.Context, int, string, uint64) (bool, error)
	Stop(context.Context, int, string) error
}
type AuthoritySnapshot struct {
	Active, Pending map[string]workerwire.Assignment
}
type SourceAuthority interface {
	Acquire(context.Context, int, string, uint64, string, string) (workerwire.Source, error)
	Release(context.Context, int, string, uint64) error
}
type RecipeStore interface {
	Get(context.Context, string) (*playback.RecipeCard, bool)
	Delete(context.Context, string) error
	RevokeNode(context.Context, string) error
}
type Options struct {
	OutputAuthorities OutputAuthorities
	Origin            string
	AllowHTTP         bool
	PrivateKey        ed25519.PrivateKey
	Nodes             NodeRegistry
	Store             AssignmentStore
	Sources           SourceAuthority
	VerifyToken       func(context.Context, string) (*streamtoken.Claims, error)
	RecipeStore       RecipeStore
	Report            func(context.Context, int, json.RawMessage) error
	ExecutionActive   func(context.Context, Node, workerwire.Assignment) bool
	ExecutionConfig   func(context.Context, int) (workerwire.ExecutionConfig, error)
	SourceHandler     http.Handler
	Admit             func(context.Context, Node, workerwire.Assignment) error
}

type GrantReply struct {
	Grant string `json:"grant"`
}
type RecipeReply struct {
	Assignment workerwire.Assignment `json:"assignment"`
	Recipe     playback.RecipeCard   `json:"recipe"`
}
