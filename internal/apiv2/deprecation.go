package apiv2

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
)

// DocsOrigin is the project-controlled documentation origin a deprecation
// links to. It shares its host with ProblemTypeOrigin: there is one
// documentation site, and a migration page under it is the only link a
// Deprecation may carry.
const DocsOrigin = "https://siloserver.org/docs/"

// Response header names of the RFC 9745 / RFC 8594 retirement flow.
const (
	DeprecationHeader = "Deprecation"
	SunsetHeader      = "Sunset"
	LinkHeader        = "Link"
)

// deprecationRel is the RFC 9745 link relation naming the migration page.
const deprecationRel = "deprecation"

// Deprecation is an operation's retirement declaration. Declaring it is the
// whole act: Register documents it (`deprecated: true` plus the
// x-silo-deprecation extension) and the listener emits the headers on every
// response of the operation, problems included. Nothing else changes; the
// operation keeps answering until a later change removes it.
type Deprecation struct {
	// At is the instant the operation became deprecated (required). It is
	// sent as `Deprecation: @<unix-seconds>`, the RFC 9745 date form.
	At time.Time
	// Link is the migration documentation page (required), an absolute https
	// URL under DocsOrigin. It is sent as `Link: <Link>; rel="deprecation"`.
	Link string
	// Sunset is the planned removal instant, if one is planned. It is sent
	// as `Sunset: <IMF-fixdate>` (RFC 8594) and must not precede At.
	Sunset *time.Time
}

// validate is the registration-time check; a failing declaration is a build
// failure, never a request failure.
func (d *Deprecation) validate() error {
	if d == nil {
		return nil
	}
	if d.At.IsZero() {
		return fmt.Errorf("deprecation has a zero At instant")
	}
	if d.Link == "" {
		return fmt.Errorf("deprecation has no Link; every deprecation names its migration page")
	}
	u, err := url.Parse(d.Link)
	if err != nil {
		return fmt.Errorf("deprecation link %q is not a URL: %w", d.Link, err)
	}
	if u.Scheme != "https" || !strings.HasPrefix(d.Link, DocsOrigin) {
		return fmt.Errorf("deprecation link %q must be an absolute https URL under %s", d.Link, DocsOrigin)
	}
	if d.Sunset != nil && d.Sunset.Before(d.At) {
		return fmt.Errorf("deprecation sunset %s precedes its deprecation instant %s",
			d.Sunset.UTC().Format(time.RFC3339), d.At.UTC().Format(time.RFC3339))
	}
	return nil
}

// FormatDeprecation renders the RFC 9745 Deprecation field value: a
// structured-field Date, `@` followed by whole unix seconds, unquoted.
func FormatDeprecation(t time.Time) string {
	return "@" + strconv.FormatInt(t.Unix(), 10)
}

// FormatSunset renders the RFC 8594 Sunset field value: an IMF-fixdate in
// GMT, the HTTP-date form.
func FormatSunset(t time.Time) string {
	return t.UTC().Format(http.TimeFormat)
}

// appendLink adds one link-value to an existing Link field value using the
// RFC 8288 list syntax: a comma-separated list of `<uri>; rel="…"`. An
// existing value is never replaced, so a link another middleware set stays.
func appendLink(existing, link, rel string) string {
	value := "<" + link + `>; rel="` + rel + `"`
	if strings.TrimSpace(existing) == "" {
		return value
	}
	return existing + ", " + value
}

// setDeprecationHeaders writes the three headers for a declaration onto the
// response. Link is appended to whatever Link value is already present.
func setDeprecationHeaders(h http.Header, d *Deprecation) {
	h.Set(DeprecationHeader, FormatDeprecation(d.At))
	existing := strings.Join(h.Values(LinkHeader), ", ")
	h.Set(LinkHeader, appendLink(existing, d.Link, deprecationRel))
	if d.Sunset != nil {
		h.Set(SunsetHeader, FormatSunset(*d.Sunset))
	}
}

// retirementHeadersKey carries a private snapshot from operation middleware to
// the outer response buffer; handlers only receive the separate response headers.
type retirementHeadersKey struct{}

// deprecationHeaders records the declaration for panic recovery, then applies it
// after downstream middleware and handlers finish. The listener buffers responses,
// so success and gate errors both receive authoritative retirement headers before
// anything reaches the client; downstream Link values remain alongside migration.
func deprecationHeaders(ctx huma.Context, next func(huma.Context)) {
	d, _ := ctx.Operation().Metadata[metaDeprecation].(*Deprecation)
	if d != nil {
		if declared, ok := ctx.Context().Value(retirementHeadersKey{}).(http.Header); ok {
			setDeprecationHeaders(declared, d)
		}
	}
	next(ctx)
	if d != nil {
		_, w := humachi.Unwrap(ctx)
		setDeprecationHeaders(w.Header(), d)
	}
}
