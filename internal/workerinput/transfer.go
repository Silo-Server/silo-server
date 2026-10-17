package workerinput

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const sourceTransferAttempts = 4

type sourceResponseError struct {
	status  int
	message string
	retire  bool
}

func (e *sourceResponseError) Error() string { return e.message }

// transfer normally uses one sequential response. An interrupted response can
// resume only its undelivered suffix, under the same grant and immutable ETag.
// Callers retain one admission slot and bind ctx to the handle lifetime.
func (h *Handle) transfer(ctx context.Context, method, byteRange string, dst io.Writer, begin func(*http.Response, int64), budget func(int64) error) error {
	start, end := int64(0), h.source.Size-1
	if byteRange != "" {
		var err error
		start, end, err = requestedRange(byteRange, h.source.Size)
		if err != nil {
			return &sourceResponseError{status: http.StatusRequestedRangeNotSatisfiable, message: "invalid source range"}
		}
	}
	cursor := start
	started := false
	buffer := make([]byte, 64<<10)
	for attempt := 0; attempt < sourceTransferAttempts; attempt++ {
		if err := h.Validate(ctx); err != nil {
			return err
		}
		remaining := end - cursor + 1
		if budget != nil {
			if err := budget(remaining); err != nil {
				return err
			}
		}
		request, err := h.sourceRequest(ctx, method, byteRange, start, cursor, end)
		if err != nil {
			return err
		}
		written, delay, err := h.transferSourceAttempt(ctx, request, dst, buffer, func(response *http.Response) {
			if !started && begin != nil {
				begin(response, end-start+1)
			}
			started = true
		})
		cursor += written
		if err != nil {
			return err
		}
		if started && (method == http.MethodHead || cursor > end) {
			return nil
		}
		retryDelay := 50 * time.Millisecond << attempt
		if delay > 0 {
			retryDelay = delay
		}
		if attempt+1 == sourceTransferAttempts {
			break
		}
		if err := h.waitSourceRetry(ctx, retryDelay); err != nil {
			return err
		}
	}
	return &sourceResponseError{status: http.StatusServiceUnavailable, message: "source temporarily unavailable"}
}

func (h *Handle) transferSourceAttempt(ctx context.Context, request *http.Request, dst io.Writer, buffer []byte, begin func(*http.Response)) (int64, time.Duration, error) {
	response, err := h.adapter.client.Do(request) //nolint:gosec // Open admits an exact configured source origin and the client refuses redirects.
	if err != nil {
		return 0, 0, sourceRequestFailure(ctx, err)
	}
	retry, delay, err := sourceResponseStatus(response)
	if err != nil || retry {
		return 0, delay, err
	}
	written, err := h.copySourceResponse(ctx, request, response, dst, buffer, func() { begin(response) })
	return written, 0, err
}

func (h *Handle) sourceRequest(ctx context.Context, method, byteRange string, start, cursor, end int64) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, h.source.URL, nil)
	if err != nil {
		return nil, &sourceResponseError{status: http.StatusBadGateway, message: "invalid source request"}
	}
	request.Header.Set("X-Silo-Source-Grant", h.source.Grant)
	request.Header.Set("If-Match", h.source.ETag)
	request.Header.Set("Accept-Encoding", "identity")
	if cursor > start {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", cursor, end))
	} else if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	if h.adapter.options.Sign != nil {
		if err := h.adapter.options.Sign(request); err != nil {
			return nil, &sourceResponseError{status: http.StatusBadGateway, message: "source proof unavailable"}
		}
	}
	return request, nil
}

func sourceRequestFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return &sourceResponseError{status: http.StatusBadGateway, message: "source TLS verification failed"}
	}
	return nil
}

// A retryable status consumes its body here; successful responses remain owned
// by copySourceResponse, which validates the representation before delivery.
func sourceResponseStatus(response *http.Response) (bool, time.Duration, error) {
	status := response.StatusCode
	if status == http.StatusForbidden || status == http.StatusGone || status == http.StatusPreconditionFailed {
		_ = response.Body.Close()
		return false, 0, &sourceResponseError{status: status, message: "source authority changed", retire: true}
	}
	if status == http.StatusTooManyRequests || status >= 500 && status <= 599 {
		var delay time.Duration
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
			delay = time.Duration(min(seconds, 1)) * time.Second
		}
		_ = response.Body.Close()
		return true, delay, nil
	}
	if status < 200 || status >= 300 {
		_ = response.Body.Close()
		if status == http.StatusRequestedRangeNotSatisfiable {
			return false, 0, &sourceResponseError{status: status, message: "source range unavailable"}
		}
		return false, 0, &sourceResponseError{status: http.StatusBadGateway, message: "source request refused"}
	}
	return false, 0, nil
}

func (h *Handle) copySourceResponse(ctx context.Context, request *http.Request, response *http.Response, dst io.Writer, buffer []byte, begin func()) (int64, error) {
	length, err := validateResponse(request, response, h.source)
	if err != nil {
		_ = response.Body.Close()
		return 0, &sourceResponseError{status: http.StatusBadGateway, message: "invalid source representation", retire: true}
	}
	if err := h.Validate(ctx); err != nil {
		_ = response.Body.Close()
		return 0, err
	}
	begin()
	if request.Method == http.MethodHead {
		return 0, response.Body.Close()
	}
	defer func() { _ = response.Body.Close() }()
	var delivered int64
	for length > 0 {
		if err := h.Validate(ctx); err != nil {
			return delivered, err
		}
		n, readErr := response.Body.Read(buffer[:min(int64(len(buffer)), length)])
		if n > 0 {
			if err := h.Validate(ctx); err != nil {
				return delivered, err
			}
			written, writeErr := dst.Write(buffer[:n])
			delivered += int64(written)
			length -= int64(written)
			if writeErr != nil {
				return delivered, writeErr
			}
			if written != n {
				return delivered, io.ErrShortWrite
			}
		}
		if readErr != nil || n == 0 {
			break
		}
	}
	return delivered, nil
}

func (h *Handle) waitSourceRetry(ctx context.Context, delay time.Duration) error {
	if err := h.Validate(ctx); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.ctx.Done():
		return h.ctx.Err()
	case <-timer.C:
		return h.Validate(ctx)
	}
}
