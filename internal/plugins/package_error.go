package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// PackageError is an install or update refused because of the plugin package
// itself or the catalog that lists it: a failed download, a checksum
// mismatch, an unsupported platform or API version, or a binary that is not
// the plugin its catalog lists. Message is written for the admin who asked for
// the install and names no URL or checksum; Err keeps the detail for logs.
// Transient marks a failure to reach the catalog or download host, which the
// same request may get past later; every other PackageError recurs until the
// package or catalog changes.
type PackageError struct {
	Message   string
	Transient bool
	Err       error
}

func (e *PackageError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

func (e *PackageError) Unwrap() error { return e.Err }

func packageError(err error, format string, args ...any) error {
	return &PackageError{Message: fmt.Sprintf(format, args...), Err: err}
}

func transientPackageError(err error, format string, args ...any) error {
	return &PackageError{Message: fmt.Sprintf(format, args...), Transient: true, Err: err}
}

// AsPackageError returns err's PackageError, if it has one.
func AsPackageError(err error) (*PackageError, bool) {
	var packageErr *PackageError
	if !errors.As(err, &packageErr) {
		return nil, false
	}
	return packageErr, true
}

// fetchPackageResource GETs a catalog index, checksum file or plugin
// download. subject names it for the admin, such as "the plugin". An
// unreachable host, 429, 5xx or a cut-off body is a transient PackageError;
// an address that isn't a valid http or https link, or any other status, is
// a permanent one. A canceled ctx returns its own error, not a PackageError.
func fetchPackageResource(ctx context.Context, client *http.Client, rawURL, subject string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, invalidPackageAddressError(fmt.Errorf("build request for %q: %w", rawURL, err), subject)
	}
	// The transport refuses any other address before connecting, and a retry
	// can't change that.
	if (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" {
		return nil, invalidPackageAddressError(fmt.Errorf("fetch %q: not an http or https address", rawURL), subject)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("fetch %q: %w", rawURL, ctx.Err())
		}
		return nil, transientPackageError(fmt.Errorf("fetch %q: %w", rawURL, err),
			"Silo couldn't reach the host for %s. Check that this server can reach it, then try again.", subject)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("fetch %q: unexpected status %d", rawURL, resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
			return nil, transientPackageError(statusErr, "Silo couldn't download %s: its host answered HTTP %d. Try again in a moment.", subject, resp.StatusCode)
		}
		return nil, packageError(statusErr, "Silo couldn't download %s: its host answered HTTP %d.", subject, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("read %q: %w", rawURL, ctx.Err())
		}
		return nil, transientPackageError(fmt.Errorf("read %q: %w", rawURL, err), "The download of %s was interrupted. Try again.", subject)
	}
	return data, nil
}

func unsupportedAPIVersionError(version, supported string) error {
	return packageError(fmt.Errorf("plugin silo_api_version %q is not supported", version),
		"This plugin needs Silo plugin API %q, and this server supports %q.", version, supported)
}

func invalidPackageAddressError(err error, subject string) error {
	return packageError(err, "Silo can't download %s: its address isn't a valid http or https link.", subject)
}
