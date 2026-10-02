package mediasample

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/telemetry"
)

// RemotePath is the transcode-node route that runs a Request with the node's
// own ffmpeg and returns its Result (see docs/architecture/media-sampling.md,
// Remote runs).
const RemotePath = "/media-samples/run"

// MaxRemoteRequestBytes bounds a remote request body. A Samples list of the
// largest allowed size fits with room to spare.
const MaxRemoteRequestBytes = 1 << 20

// MaxRemoteAdmissionWait is how long a node holds a request waiting for a
// free sampling slot before refusing it as unavailable. A caller's request
// deadline covers this wait on top of the run's own attempt timeouts.
const MaxRemoteAdmissionWait = 2 * time.Minute

// maxRemoteResultBytes bounds a remote result: speech levels for a whole
// feature film are about 1 MB, base64 in JSON.
const maxRemoteResultBytes = 64 << 20

// RemoteFailure is the JSON body of a failed remote run.
type RemoteFailure struct {
	// Reason is the run's Classify cause, or "invalid_request" and
	// "node_unavailable" for failures before ffmpeg ran.
	Reason Reason `json:"reason"`
	Error  string `json:"error,omitempty"`
}

// Remote failure reasons for refusals before ffmpeg runs.
const (
	ReasonInvalidRequest  Reason = "invalid_request"
	ReasonNodeUnavailable Reason = "node_unavailable"
)

// RemoteError is a remote run that did not return a Result.
type RemoteError struct {
	// Status is the node's HTTP status, zero when no response arrived.
	Status int
	Reason Reason
	Err    error
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("remote media sample (status %d, %s): %v", e.Status, e.Reason, e.Err)
}

func (e *RemoteError) Unwrap() error { return e.Err }

// Infrastructure reports whether the failure lies with the node or the path
// to it rather than with the file: running the same request elsewhere may
// succeed. A run ffmpeg itself failed (422) is not one unless the node lacked
// a capability.
func (e *RemoteError) Infrastructure() bool {
	if e.Status == http.StatusBadRequest {
		// The node refuses a malformed request with a RemoteFailure; a bare
		// 400 is its input-path authority, which says only that this node
		// cannot read the file.
		return e.Reason != ReasonInvalidRequest
	}
	if e.Status != http.StatusUnprocessableEntity {
		return true
	}
	switch e.Reason {
	case ReasonUnsupported, ReasonCapabilities, ReasonStart, ReasonKilled:
		return true
	}
	return false
}

// RemoteClient runs requests on transcode nodes.
type RemoteClient struct {
	// HTTP is the client to use; nil uses http.DefaultClient. The caller's
	// context bounds each run.
	HTTP *http.Client
}

// Run sends req to the node route at endpoint (the node URL joined with
// RemotePath) with the node bearer secret. A failure is a *RemoteError.
func (c RemoteClient) Run(ctx context.Context, endpoint, secret string, req Request) (Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, &RemoteError{Reason: ReasonInvalidRequest, Err: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, &RemoteError{Reason: ReasonNodeUnavailable, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+secret)
	resp, err := telemetry.DoTrustedNode(c.HTTP, httpReq, "media_sample")
	if err != nil {
		reason := ReasonNodeUnavailable
		if ctx.Err() != nil {
			reason = ReasonCanceled
		}
		return Result{}, &RemoteError{Reason: reason, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteResultBytes+1))
	if err != nil {
		return Result{}, &RemoteError{Status: resp.StatusCode, Reason: ReasonNodeUnavailable, Err: fmt.Errorf("read response: %w", err)}
	}
	if len(data) > maxRemoteResultBytes {
		return Result{}, &RemoteError{Status: resp.StatusCode, Reason: ReasonNodeUnavailable, Err: errors.New("response exceeds size limit")}
	}
	if resp.StatusCode != http.StatusOK {
		failure := RemoteFailure{Reason: ReasonNodeUnavailable, Error: http.StatusText(resp.StatusCode)}
		_ = json.Unmarshal(data, &failure)
		return Result{}, &RemoteError{Status: resp.StatusCode, Reason: failure.Reason, Err: errors.New(failure.Error)}
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return Result{}, &RemoteError{Status: resp.StatusCode, Reason: ReasonNodeUnavailable, Err: fmt.Errorf("decode result: %w", err)}
	}
	return result, nil
}
