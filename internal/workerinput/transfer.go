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
		request, err := http.NewRequestWithContext(ctx, method, h.source.URL, nil)
		if err != nil {
			return &sourceResponseError{status: http.StatusBadGateway, message: "invalid source request"}
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
				return &sourceResponseError{status: http.StatusBadGateway, message: "source proof unavailable"}
			}
		}
		response, err := h.adapter.client.Do(request)
		retryDelay := 50 * time.Millisecond << attempt
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var unknown x509.UnknownAuthorityError
			var hostname x509.HostnameError
			var invalid x509.CertificateInvalidError
			if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
				return &sourceResponseError{status: http.StatusBadGateway, message: "source TLS verification failed"}
			}
		} else {
			status := response.StatusCode
			if status == http.StatusForbidden || status == http.StatusGone || status == http.StatusPreconditionFailed {
				_ = response.Body.Close()
				return &sourceResponseError{status: status, message: "source authority changed", retire: true}
			}
			if status == http.StatusTooManyRequests || status >= 500 && status <= 599 {
				if seconds, e := strconv.Atoi(response.Header.Get("Retry-After")); e == nil && seconds > 0 {
					retryDelay = time.Duration(min(seconds, 1)) * time.Second
				}
				_ = response.Body.Close()
			} else if status < 200 || status >= 300 {
				_ = response.Body.Close()
				if status == http.StatusRequestedRangeNotSatisfiable {
					return &sourceResponseError{status: status, message: "source range unavailable"}
				}
				return &sourceResponseError{status: http.StatusBadGateway, message: "source request refused"}
			} else {
				length, validationErr := validateResponse(request, response, h.source)
				if validationErr != nil {
					_ = response.Body.Close()
					return &sourceResponseError{status: http.StatusBadGateway, message: "invalid source representation", retire: true}
				}
				if err := h.Validate(ctx); err != nil {
					_ = response.Body.Close()
					return err
				}
				if !started {
					if begin != nil {
						begin(response, end-start+1)
					}
					started = true
				}
				if method == http.MethodHead {
					return response.Body.Close()
				}
				for length > 0 {
					if err := h.Validate(ctx); err != nil {
						_ = response.Body.Close()
						return err
					}
					n, readErr := response.Body.Read(buffer[:min(int64(len(buffer)), length)])
					if n > 0 {
						if err := h.Validate(ctx); err != nil {
							_ = response.Body.Close()
							return err
						}
						written, writeErr := dst.Write(buffer[:n])
						cursor += int64(written)
						length -= int64(written)
						if writeErr != nil || written != n {
							_ = response.Body.Close()
							if writeErr != nil {
								return writeErr
							}
							return io.ErrShortWrite
						}
					}
					if readErr != nil {
						break
					}
					if n == 0 {
						break
					}
				}
				_ = response.Body.Close()
				if cursor > end {
					return nil
				}
			}
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
