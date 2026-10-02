package logredact

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

const invalidURLPlaceholder = "[invalid URL]"

// SanitizeURL returns a diagnostic-safe URL with credential-bearing
// components removed. Malformed and non-hierarchical URLs fail closed rather
// than returning any part of the untrusted input.
func SanitizeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return invalidURLPlaceholder
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

// SanitizeURLError clones an HTTP client's URL error while removing secrets
// from every nested requested URL. The underlying non-URL cause is preserved
// so errors.Is and useful transport diagnostics continue to work. When the
// URL error is nested inside another wrapper (fmt.Errorf or errors.Join), the
// surrounding chain and its diagnostic text are preserved as well.
func SanitizeURLError(err error) error {
	if err == nil {
		return nil
	}
	// Multi-error chains (errors.Join, multi-%w fmt.Errorf): sanitize every
	// component and rebuild the outer message by substituting each changed
	// component's raw text with its sanitized text, so a fmt.Errorf format
	// like "a: %w; b: %w" keeps its shape instead of collapsing into
	// errors.Join's newline-joined message. errors.Join is the fallback for
	// multi-errors whose message does not contain a component's text.
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		if components := multi.Unwrap(); len(components) > 0 {
			return sanitizeMultiURLError(err, components)
		}
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || urlErr == nil {
		return err
	}
	clone := *urlErr
	clone.URL = SanitizeURL(urlErr.URL)
	clone.Err = SanitizeURLError(urlErr.Err)
	// The identity check is deliberate: errors.As already located the nested
	// URL error, and this distinguishes a direct *url.Error (the clone is the
	// whole chain) from a wrapper that must keep its surrounding message.
	if _, ok := err.(*url.Error); ok { //nolint:errorlint
		// A direct *url.Error: the sanitized clone is the whole chain.
		return &clone
	}
	// A single-cause wrapper keeps its outer message with the sanitized URL
	// error re-attached as the cause. The raw URL error text is replaced
	// wherever it appears — prefix, suffix, or middle — so a wrapper like
	// fmt.Errorf("%w: extra", urlErr) cannot leak the original URL. When the
	// raw text is absent, fall back to the sanitized clone's text so the
	// message can never carry the raw URL.
	outer := err.Error()
	switch {
	case strings.Contains(outer, urlErr.Error()):
		outer = strings.ReplaceAll(outer, urlErr.Error(), clone.Error())
	case urlErr.URL != "" && strings.Contains(outer, urlErr.URL):
		outer = strings.ReplaceAll(outer, urlErr.URL, clone.URL)
	default:
		outer = clone.Error()
	}
	return &sanitizedWrapperError{message: outer, cause: &clone}
}

// sanitizeMultiURLError rebuilds a multi-error's message in place: each
// component whose chain contains a *url.Error is replaced by its sanitized
// equivalent, and its raw text is substituted wherever it appears in the outer
// message. Longest-first substitution keeps a short component's text from
// being rewritten inside a longer component's text.
func sanitizeMultiURLError(original error, components []error) error {
	order := make([]int, len(components))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return len(components[order[a]].Error()) > len(components[order[b]].Error())
	})
	sanitized := make([]error, len(components))
	outer := original.Error()
	allPresent := true
	for _, idx := range order {
		component := components[idx]
		var urlErr *url.Error
		if !errors.As(component, &urlErr) {
			sanitized[idx] = component
			continue
		}
		sanitized[idx] = SanitizeURLError(component)
		if strings.Contains(outer, component.Error()) {
			outer = strings.ReplaceAll(outer, component.Error(), sanitized[idx].Error())
		} else {
			allPresent = false
		}
	}
	if !allPresent {
		return errors.Join(sanitized...)
	}
	if outer == original.Error() {
		return original
	}
	return &sanitizedMultiError{message: outer, unwrap: sanitized}
}

// sanitizedWrapperError preserves a single-cause wrapper's outer message
// while unwrapping to the sanitized URL error clone, so errors.Is and
// errors.As keep working through the chain.
type sanitizedWrapperError struct {
	message string
	cause   error
}

func (e *sanitizedWrapperError) Error() string { return e.message }
func (e *sanitizedWrapperError) Unwrap() error { return e.cause }

// sanitizedMultiError preserves a multi-error's outer message while unwrapping
// to its sanitized components.
type sanitizedMultiError struct {
	message string
	unwrap  []error
}

func (e *sanitizedMultiError) Error() string   { return e.message }
func (e *sanitizedMultiError) Unwrap() []error { return e.unwrap }
