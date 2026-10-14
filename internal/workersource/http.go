package workersource

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

func strconvNode(node int) string { return strconv.Itoa(node) }

const sourceWatchInterval = 250 * time.Millisecond
const sourceCheckTimeout = time.Second
const sourceWriteHorizon = 2 * time.Second

func (a *Authority) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Encoding", "identity")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sourceError(w, r, 405)
		return
	}
	if !strings.HasPrefix(r.URL.Path, a.routePrefix) || r.URL.RawQuery != "" || r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		sourceError(w, r, 404)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, a.routePrefix), "/")
	if len(parts) != 3 || parts[1] != "sources" {
		sourceError(w, r, 404)
		return
	}
	node, err := strconv.Atoi(parts[0])
	if err != nil || node < 1 || strconv.Itoa(node) != parts[0] {
		sourceError(w, r, 404)
		return
	}
	parsed, err := uuid.Parse(parts[2])
	if err != nil || parsed.String() != parts[2] {
		sourceError(w, r, 404)
		return
	}
	if err := a.options.VerifyRequest(r, node); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusServiceUnavailable
		}
		sourceError(w, r, status)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Silo-Source-Grant")), []byte(parts[2])) != 1 {
		sourceError(w, r, 403)
		return
	}
	if err := r.Context().Err(); err != nil {
		sourceError(w, r, 408)
		return
	}
	record, err := a.options.Store.Get(r.Context(), parts[2])
	if err != nil {
		sourceError(w, r, sourceStatus(err))
		return
	}
	if record.Source.NodeID != node || record.Source.Origin != a.options.Origin {
		sourceError(w, r, 403)
		return
	}
	if !a.options.Now().Before(record.ExpiresAt) {
		sourceError(w, r, 410)
		return
	}
	if err := a.checkActive(r.Context(), node, record.Source.WorkID, record.Source.Generation); err != nil {
		sourceError(w, r, HTTPStatus(err))
		return
	}
	if err := a.options.PathApprover(r.Context(), record.Path, record.Source.Ref.Kind); err != nil {
		sourceError(w, r, HTTPStatus(err))
		return
	}
	match := r.Header.Get("If-Match")
	if (r.Method == http.MethodGet || match != "") && match != record.Source.ETag {
		sourceError(w, r, 412)
		return
	}
	select {
	case a.readers <- struct{}{}:
		defer func() { <-a.readers }()
	default:
		w.Header().Set("Retry-After", "1")
		sourceError(w, r, 429)
		return
	}
	f, identity, err := openPin(record.Path)
	if err == nil && identity != record.Identity {
		err = ErrChanged
	}
	if err != nil {
		err = a.invalidateChanged(r.Context(), record.Source.Grant, err)
		sourceError(w, r, HTTPStatus(err))
		if f != nil {
			_ = f.Close()
		}
		return
	}
	defer func() { _ = f.Close() }()
	start, length, status := int64(0), record.Source.Size, 200
	if r.Method == http.MethodGet && r.Header.Get("Range") != "" {
		start, length, status = singleRange(r.Header.Get("Range"), record.Source.Size)
		if status >= 400 {
			if status == 416 {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", record.Source.Size))
			}
			sourceError(w, r, status)
			return
		}
	}
	w.Header().Set("ETag", record.Source.ETag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if status == 206 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, record.Source.Size))
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	buffer := make([]byte, a.options.Limits.ChunkBytes)
	streamCtx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	var authorityMu sync.Mutex
	lastSharedCheck := a.options.Now()
	authorized := func() bool {
		checkCtx, cancel := context.WithTimeout(streamCtx, sourceCheckTimeout)
		defer cancel()
		if checkCtx.Err() != nil {
			return false
		}
		authorityMu.Lock()
		now := a.options.Now()
		// A long GET can outlive its initial lease when the control plane renews
		// it. Refresh before rejecting that snapshot, without a store operation
		// for every chunk. Deletion on another core is observed within a second.
		if now.Sub(lastSharedCheck) >= time.Second || !now.Before(record.ExpiresAt) {
			current, e := a.options.Store.Get(checkCtx, record.Source.Grant)
			if e != nil || current.Source != record.Source {
				authorityMu.Unlock()
				return false
			}
			record.ExpiresAt = current.ExpiresAt
			lastSharedCheck = a.options.Now()
		}
		unexpired := a.options.Now().Before(record.ExpiresAt)
		authorityMu.Unlock()
		return unexpired && a.checkActive(checkCtx, node, record.Source.WorkID, record.Source.Generation) == nil && checkCtx.Err() == nil
	}
	invalidate := func(err error) {
		if streamCtx.Err() == nil {
			ctx, cancel := context.WithTimeout(streamCtx, sourceCheckTimeout)
			defer cancel()
			_ = a.invalidateChanged(ctx, record.Source.Grant, err)
		}
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(sourceWriteHorizon))
	stopWatch, watchDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(sourceWatchInterval)
		defer ticker.Stop()
		interrupt := func() {
			cancelStream()
			_ = controller.SetWriteDeadline(time.Now())
			_ = f.Close()
		}
		for {
			select {
			case <-stopWatch:
				return
			case <-streamCtx.Done():
				interrupt()
				return
			case <-ticker.C:
				if !authorized() {
					interrupt()
					return
				}
				if err := validatePin(f, record.Path, record.Identity); err != nil {
					invalidate(err)
					interrupt()
					return
				}
				if streamCtx.Err() != nil {
					interrupt()
					return
				}
				// Healthy backpressure has no fixed transfer timeout. Only this
				// watcher renews the safety horizon or expires it on authority loss.
				_ = controller.SetWriteDeadline(time.Now().Add(sourceWriteHorizon))
			}
		}
	}()
	defer func() {
		normal := streamCtx.Err() == nil
		close(stopWatch)
		cancelStream()
		<-watchDone
		if normal {
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}()
	w.WriteHeader(status)
	for offset, remaining := start, length; remaining > 0; {
		if !authorized() {
			return
		}
		if err := validatePin(f, record.Path, record.Identity); err != nil {
			invalidate(err)
			return
		}
		count := min(int64(len(buffer)), remaining)
		n, readErr := f.ReadAt(buffer[:count], offset)
		if streamCtx.Err() != nil {
			// Canceling one speculative fetch does not revoke its work's grant.
			return
		}
		if readErr != nil || int64(n) != count {
			// Failed storage reads do not establish a new representation. A
			// successful unequal stat remains a definitive mutation fence.
			if err := validatePin(f, record.Path, record.Identity); err != nil {
				invalidate(err)
			}
			return
		}
		if err := validatePin(f, record.Path, record.Identity); err != nil {
			invalidate(err)
			return
		}
		if !authorized() {
			return
		}
		written, writeErr := w.Write(buffer[:n])
		if writeErr != nil || written != n {
			return
		}
		offset += int64(n)
		remaining -= int64(n)
	}
	if err := validatePin(f, record.Path, record.Identity); err != nil {
		invalidate(err)
	}
}

func sourceStatus(err error) int {
	return HTTPStatus(err)
}

// HTTPStatus keeps dependency failure distinct from definite authority loss.
func HTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrExpired), errors.Is(err, ErrChanged):
		return 410
	case errors.Is(err, ErrDenied), errors.Is(err, ErrInactive):
		return 403
	case errors.Is(err, ErrQuota):
		return 429
	default:
		return 503
	}
}
func sourceError(w http.ResponseWriter, r *http.Request, status int) {
	if status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	http.Error(w, http.StatusText(status), status)
}

func singleRange(header string, size int64) (int64, int64, int) {
	if len(header) > 256 || !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, 400
	}
	parts := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
	if len(parts) != 2 || (parts[0] == "" && parts[1] == "") {
		return 0, 0, 400
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, 0, 400
			}
		}
	}
	if size == 0 {
		return 0, 0, 416
	}
	if parts[0] == "" {
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n < 0 {
			return 0, 0, 400
		}
		if n == 0 {
			return 0, 0, 416
		}
		n = min(n, size)
		return size - n, n, 206
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return 0, 0, 400
	}
	if start >= size {
		return 0, 0, 416
	}
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, 400
		}
		end = min(end, size-1)
	}
	return start, end - start + 1, 206
}
