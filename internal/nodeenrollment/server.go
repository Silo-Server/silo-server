package nodeenrollment

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
)

// Server admits exactly one binding before the database-backed runtime starts.
// Validation is injected so this listener remains independent of that runtime.
type Server struct {
	token    [32]byte
	store    Store
	validate func(context.Context, Binding) error
	mu       sync.Mutex
	bound    bool
	done     chan Binding
}

func NewServer(token string, store Store, validate func(context.Context, Binding) error) (*Server, error) {
	if len(token) < 32 || len(token) > 512 || store.Path == "" || validate == nil {
		return nil, errors.New("enrollment requires a provider token, binding path and validator")
	}
	return &Server{token: sha256.Sum256([]byte(token)), store: store, validate: validate, done: make(chan Binding, 1)}, nil
}
func (s *Server) Done() <-chan Binding { return s.done }

type sealedHandler struct{ h http.Handler }

func (h sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.h.ServeHTTP(w, r) }
func (s *Server) Handler() http.Handler                                  { return sealedHandler{h: s.router()} }
func (s *Server) router() chi.Router {
	r := chi.NewRouter()
	r.Get("/api/v1/health", s.handleHealth)
	r.Group(func(r chi.Router) { r.Use(s.requireBearer); r.Post("/api/v1/node/enroll", s.enroll) })
	return r
}
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Status           string                 `json:"status"`
		EncodingCapacity encodinglimit.Capacity `json:"encoding_capacity"`
	}{"unbound", encodinglimit.Snapshot()})
}

// requireBearer checks the provider's one-time enrollment token, not the
// customer JWT secret used by an already-bound worker.
func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		token := sha256.Sum256([]byte(raw))
		if !ok || subtle.ConstantTimeCompare(token[:], s.token[:]) != 1 {
			http.Error(w, "invalid enrollment token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		http.Error(w, "invalid enrollment payload", http.StatusBadRequest)
		return
	}
	b, err := decodeBinding(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bound {
		http.Error(w, ErrAlreadyBound.Error(), http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.validate(ctx, b); err != nil {
		http.Error(w, "customer configuration could not be validated; check worker access to the database and Redis", http.StatusUnprocessableEntity)
		return
	}
	if err := s.store.Save(b); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrAlreadyBound) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.bound = true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Receipt{BindingID: Identity(b.SecretKey), EncodingCapacity: encodinglimit.Snapshot()})
	s.done <- b
}
