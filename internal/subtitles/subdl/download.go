package subdl

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// downloadPathPattern is the relative archive path SubDL search returns in
// each subtitle's url field, for example /subtitle/3197651-3213944.zip.
var downloadPathPattern = regexp.MustCompile(`^/subtitle/[0-9]+-[0-9]+\.zip$`)

var errInvalidID = fmt.Errorf("subdl: %w", subtitles.ErrInvalidSubtitleID)

// resultID turns the url of a SubDL search row into the result ID clients
// see. SubDL can sign that url with the account's api_key as a query string,
// and result IDs go to every user who can search, so only the path is kept.
func resultID(rowURL string) string {
	path, _, _ := strings.Cut(rowURL, "?")
	path, _, _ = strings.Cut(path, "#")
	return path
}

// downloadURL resolves a search result ID against the download base. The ID
// arrives from API clients, so anything other than a SubDL archive path is
// refused before any request is made, and the result must stay on the base's
// scheme, host and (absent) user-info.
func (p *Provider) downloadURL(id string) (string, error) {
	ref, err := url.Parse(id)
	if err != nil || ref.Scheme != "" || ref.User != nil || ref.Host != "" || strings.Contains(id, "#") ||
		!downloadPathPattern.MatchString(ref.Path) {
		return "", errInvalidID
	}
	// IDs issued before resultID dropped the query may still carry a signed
	// query string. Its values are never forwarded; the server signs with its
	// own key instead.
	if _, err := url.ParseQuery(ref.RawQuery); err != nil {
		return "", errInvalidID
	}
	base, err := url.Parse(p.dlBaseURL)
	if err != nil {
		return "", fmt.Errorf("subdl: parse download base: %w", err)
	}
	target := base.JoinPath(ref.Path)
	target.RawQuery = ""
	if p.apiKey != "" {
		target.RawQuery = url.Values{"api_key": {p.apiKey}}.Encode()
	}
	if !sameOrigin(target, base) || target.User.String() != base.User.String() {
		return "", errInvalidID
	}
	return target.String(), nil
}

// sameOriginRedirects lets a provider request follow redirects only on the
// scheme and host it started on, so a reply from SubDL cannot steer the
// server to another host.
func sameOriginRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("subdl: stopped after 10 redirects")
	}
	if !sameOrigin(req.URL, via[0].URL) || req.URL.User != nil {
		return fmt.Errorf("subdl: refused redirect off %s", via[0].URL.Host)
	}
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
