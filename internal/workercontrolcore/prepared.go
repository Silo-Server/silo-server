package workercontrolcore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const receiptOriginal = "receipt:original"
const receiptWorker = "receipt:worker"

func (s *Service) receipt(ctx context.Context, a workerwire.Assignment) (string, string, error) {
	binder, ok := s.options.Store.(InputBinder)
	if !ok {
		return "", "", ErrUnavailable
	}
	bindings, err := binder.InputBindings(ctx, a.NodeID, a.WorkID, a.Generation)
	if err != nil {
		return "", "", err
	}
	original, worker := bindings[receiptOriginal], bindings[receiptWorker]
	if len(original) != 64 || len(worker) != 64 {
		return "", "", ErrDenied
	}
	return original, worker, nil
}
func (s *Service) verifyPreparedResponse(ctx context.Context, a workerwire.Assignment, response *http.Response, cancel context.CancelFunc) error {
	original, expected, err := s.receipt(ctx, a)
	if err != nil {
		return err
	}
	if response.Body == nil {
		return ErrDenied
	}
	// The long encode is allowed to wait for headers, but its tiny result cannot
	// hold dispatch indefinitely. Canceling the transport context interrupts a
	// network body stalled after success headers.
	timer := time.AfterFunc(3*time.Second, cancel)
	defer timer.Stop()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	_ = response.Body.Close()
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return ErrDenied
	}
	var result downloadprepare.Result
	if err = json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.ArtifactID != a.WorkID || result.FileSize <= 0 || result.ExecutionFingerprint != expected {
		return ErrDenied
	}
	// Only a receipt matching the opaque source/complete byte recipe may be
	// translated back to the original core-only request's legacy fingerprint.
	result.ExecutionFingerprint = original
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	response.Body = io.NopCloser(bytes.NewReader(encoded))
	response.ContentLength = int64(len(encoded))
	response.TransferEncoding = nil
	if response.Header == nil {
		response.Header = http.Header{}
	}
	response.Header.Set("Content-Length", strconv.Itoa(len(encoded)))
	downloadprepare.SetResultHeaders(response.Header, result)
	return nil
}
func (s *Service) bridgeArtifact(r *http.Request, response *http.Response) error {
	if response == nil || response.StatusCode < 200 || response.StatusCode >= 300 || (r.Method != http.MethodHead && r.Method != http.MethodGet) || !strings.HasPrefix(r.URL.Path, "/downloads/artifacts/") {
		return nil
	}
	node, registered, err := s.nodeForRequest(r)
	if err != nil {
		return err
	}
	if !registered {
		return nil
	}
	work := requestWork(r.URL.Path)
	if !validWork(work) {
		return ErrDenied
	}
	a, ok, err := s.options.Store.Get(r.Context(), node.ID, work)
	if err != nil {
		return err
	}
	if !ok || !s.WorkActive(r.Context(), node.ID, work, a.Generation) {
		return ErrDenied
	}
	original, expected, err := s.receipt(r.Context(), a)
	if err != nil {
		return err
	}
	result, err := downloadprepare.ResultFromHeaders(response.Header)
	if err != nil || result.ExecutionFingerprint != expected || result.FileSize <= 0 {
		return ErrDenied
	}
	result.ExecutionFingerprint = original
	downloadprepare.SetResultHeaders(response.Header, result)
	return nil
}

// completionBody retains synchronous background source authority through the
// entire response stream, not merely until response headers arrive.
type completionBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

func (b *completionBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.finish)
	}
	return n, err
}
func (b *completionBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.finish); return err }
func (s *Service) completeBody(response *http.Response, a workerwire.Assignment, cancel context.CancelFunc) {
	finish := func() {
		defer cancel()
		if a.Path == transcodeStartPath || a.Path == preparePath {
			s.finishAdmission(a)
			return
		}
		ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		node, ok, err := s.options.Nodes.ByID(ctx, a.NodeID)
		if err == nil && ok {
			_ = s.stop(ctx, node, a.WorkID)
		} else {
			s.invalidate(a.NodeID, a.WorkID)
		}
	}
	if a.Path == transcodeStartPath || a.Path == preparePath {
		s.finishAdmission(a)
	}
	if response.Body == nil {
		finish()
		return
	}
	response.Body = &completionBody{ReadCloser: response.Body, finish: finish}
}
