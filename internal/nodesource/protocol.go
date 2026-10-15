package nodesource

import (
	"fmt"
	"net/http"
)

// Paths of the core's source endpoints, relative to the core URL a transcode
// node is given (SILO_CORE_URL).
const (
	PathOpen    = "/api/internal/nodes/self/sources/open"
	PathSources = "/api/internal/nodes/sources"
)

// HeaderSourceGrant carries the grant of a PathSources read. It is a header,
// not part of the URL, so access logs on the way (a reverse proxy in front of
// the core) never record the grant, whose payload names the media path.
const HeaderSourceGrant = "X-Silo-Source-Grant"

// Source access modes, the node configuration's source_access.
const (
	// ModeLocal reads the approved catalog path through the node's own mount.
	ModeLocal = "local"
	// ModeCore reads the bytes over HTTP from the core.
	ModeCore = "core"
)

// opRead names a source read in errors.
const opRead = "read"

// OpenRequest is the body of POST PathOpen: the grant to exchange, a newly
// dispatched one or the node's current one when it renews.
type OpenRequest struct {
	Grant string `json:"grant"`
}

// OpenResponse is the core's answer to an OpenRequest: the same claims as a
// grant valid for another GrantLifetime, and the approved source.
type OpenResponse struct {
	Grant string `json:"grant"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	// Mtime is the modification time in whole Unix seconds, the revision's
	// precision (RevisionMtime).
	Mtime int64  `json:"mtime"`
	ETag  string `json:"etag"`
}

// StatusError is an answer from the core other than the one a source call
// expects.
type StatusError struct {
	Op     string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("nodesource: core answered %s with status %d", e.Op, e.Status)
}

// Fatal reports whether the status is the core's lasting answer: the request
// is malformed (400, which no retry heals), the grant is refused (403), the
// source is unknown or gone (404, 410), or it changed (412). Nothing retries a
// fatal status, and it fails the source handle. Anything else may pass, 401
// included: the core reloads node keys when a signature fails to verify, so a
// key rotation in progress heals within a retry or two.
func (e *StatusError) Fatal() bool {
	return fatalStatus(e.Status)
}

func fatalStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusPreconditionFailed:
		return true
	}
	return false
}
