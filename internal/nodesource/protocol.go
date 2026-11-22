package nodesource

import (
	"fmt"
	"net/http"
)

// Source access modes, the node configuration's source_access
// (nodepool.SourceAccessLocal and nodepool.SourceAccessCore on the core).
const (
	// ModeLocal reads the approved catalog path through the node's own
	// library mount.
	ModeLocal = "local"
	// ModeCore reads the source bytes over HTTP from the core, through the
	// node's loopback relay.
	ModeCore = "core"
)

// PathSources is the core's source bytes route, relative to the core URL a
// transcode node is given. The grant travels in HeaderSourceGrant.
const PathSources = "/api/internal/nodes/sources"

// HeaderSourceGrant carries the grant of a PathSources read. It is a header,
// not part of the URL, so access logs on the way (a reverse proxy in front of
// the core) never record the grant, whose payload names the media path. The
// node-to-core signature covers it (nodeauth.SourceGrantHeader).
const HeaderSourceGrant = "X-Silo-Source-Grant"

// OpenRequest is the body of the core's sources/open route: the grant to
// exchange, a newly dispatched one or the node's current one when it renews.
type OpenRequest struct {
	Grant string `json:"grant"`
}

// OpenResponse is the core's answer to an OpenRequest: the same claims as a
// grant valid for another GrantLifetime, and the approved source revision.
type OpenResponse struct {
	Grant string `json:"grant"`
	// Path is the approved, fspath-encoded catalog path.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// ModTimeUnixNano is the granted modification time in Unix nanoseconds,
	// as the core's stat returned it when the grant was minted. Revisions
	// compare it at microseconds (SameModTime).
	ModTimeUnixNano int64 `json:"mtime_ns"`
	// ETag is the revision's entity tag (Revision.ETag).
	ETag string `json:"etag"`
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
// fatal status, and it ends the source. Anything else may pass, 401 included:
// the core reloads node keys when a signature fails to verify, so a key
// rotation in progress heals within a retry or two.
func (e *StatusError) Fatal() bool {
	switch e.Status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusPreconditionFailed:
		return true
	}
	return false
}
