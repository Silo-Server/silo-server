package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// Registrar applies a node's declaration to its row and does what an
// administrator update does after a change, so every API server and proxy
// follows. *handlers.NodeHandler implements it.
type Registrar interface {
	// changed reports a declaration that changed the row or its locks.
	DeclareNode(ctx context.Context, id int, input nodepool.UpdateNodeInput) (node *nodepool.Node, changed bool, err error)
}

// HandleRegistration serves PUT /api/internal/nodes/self/registration: the
// configuration the calling node declares from its environment, applied to
// the row its key names and never to one named in the body.
func (s *Server) HandleRegistration(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.opts.Registrar == nil {
		http.Error(w, "node registration unavailable", http.StatusServiceUnavailable)
		return
	}
	var registration Registration
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegistrationBodyBytes))
	// A field this core does not know would otherwise read as "not declared"
	// and release that field's lock.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registration); err != nil {
		refuseRegistration(w, r, id, http.StatusUnprocessableEntity,
			RegistrationRefusal{Reason: RegistrationInvalid, Detail: "the registration body is not one this main server reads"})
		return
	}
	node, changed, err := s.opts.Registrar.DeclareNode(r.Context(), id, registration.Input())
	if err != nil {
		s.registrationFailed(w, r, id, err)
		return
	}
	envManaged := node.EnvManaged
	if envManaged == nil {
		envManaged = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(RegistrationResult{
		Node:       NodeIdentity{ID: node.ID, Name: node.Name, URL: node.URL},
		EnvManaged: envManaged,
		Changed:    changed,
	}); err != nil {
		slog.DebugContext(r.Context(), "write node registration", "component", "nodecontrol", "node_id", id, "error", err)
	}
}

// refuseRegistration answers a registration the core will not apply.
func refuseRegistration(w http.ResponseWriter, r *http.Request, id, status int, refusal RegistrationRefusal) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(refusal); err != nil {
		slog.DebugContext(r.Context(), "write node registration refusal", "component", "nodecontrol", "node_id", id, "error", err)
	}
}

func (s *Server) registrationFailed(w http.ResponseWriter, r *http.Request, id int, err error) {
	refuse := func(status int, refusal RegistrationRefusal) { refuseRegistration(w, r, id, status, refusal) }
	if declErr, ok := errors.AsType[*nodepool.DeclarationError](err); ok {
		refuse(http.StatusUnprocessableEntity, RegistrationRefusal{Reason: RegistrationInvalid, Field: declErr.Field, Detail: declErr.Detail})
		return
	}
	if errors.Is(err, nodepool.ErrInvalidNodeInput) {
		refuse(http.StatusUnprocessableEntity, RegistrationRefusal{Reason: RegistrationInvalid, Detail: "invalid node configuration"})
		return
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); errors.Is(err, nodepool.ErrTranscodeNodeURLTaken) ||
		(ok && pgErr.Code == "23505" && pgErr.ConstraintName == "stream_nodes_url_key") {
		refuse(http.StatusConflict, RegistrationRefusal{Reason: RegistrationURLTaken, Field: nodepool.FieldURL, Detail: "another node already has this URL"})
		return
	}
	if errors.Is(err, nodepool.ErrNodeNotFound) {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	slog.WarnContext(r.Context(), "apply node registration", "component", "nodecontrol", "node_id", id, "error", err)
	http.Error(w, "node registration unavailable", http.StatusServiceUnavailable)
}

// maxRegistrationBytes bounds the registration answer a node reads.
const maxRegistrationBytes = 64 << 10

// RegistrationError is a registration the core refused: its RegistrationRefusal
// with the status it came with.
type RegistrationError struct {
	Status int
	RegistrationRefusal
}

func (e *RegistrationError) Error() string {
	msg := "core refused this node's registration (" + e.Reason + ")"
	if e.Field != "" {
		msg += " for " + e.Field
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Register sends the node's declaration. A 409 or 422 with a refusal body is
// a *RegistrationError; any other answer but 200 is a StatusError, so the
// caller can tell an older core (404 or 405) from one that is down.
func (c *Client) Register(ctx context.Context, registration Registration) (*RegistrationResult, error) {
	body, _ := json.Marshal(registration) // strings and numbers only
	resp, err := c.do(ctx, http.MethodPut, PathRegistration, body, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusConflict, http.StatusUnprocessableEntity:
		var refusal RegistrationRefusal
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&refusal) == nil && refusal.Reason != "" {
			return nil, &RegistrationError{Status: resp.StatusCode, RegistrationRefusal: refusal}
		}
		return nil, &StatusError{Path: PathRegistration, Status: resp.StatusCode}
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &StatusError{Path: PathRegistration, Status: resp.StatusCode}
	}
	var result RegistrationResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRegistrationBytes)).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
