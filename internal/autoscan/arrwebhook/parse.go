// Package arrwebhook parses Sonarr/Radarr webhook payloads into autoscan
// changes. It is deliberately host-side and handler-free: input is the raw
// request body, output is the provider/event classification plus the file or
// subtree paths to feed autoscan.Service.IngestChanges. Payload-derived
// content never appears in returned error messages, so they are safe to log
// and to echo in HTTP responses.
package arrwebhook

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

// Provider names accepted by Parse and stored in source_config.webhook_provider.
const (
	ProviderAuto   = "auto"
	ProviderSonarr = "sonarr"
	ProviderRadarr = "radarr"
)

// ErrMalformedPayload reports a body that is not valid JSON or has no
// eventType. Handlers map it to 400.
var ErrMalformedPayload = errors.New("arrwebhook: malformed payload")

// ErrUnknownProvider reports a payload whose provider could not be inferred in
// auto mode. Handlers map it to 400.
var ErrUnknownProvider = errors.New("arrwebhook: cannot infer provider from payload")

// ParsedWebhook is one classified delivery. Unsupported event types parse
// successfully with no changes (callers no-op them): arr adds event types over
// time and an unknown one must not make the webhook look unhealthy.
type ParsedWebhook struct {
	Provider  string
	EventType string
	Test      bool
	Changes   []autoscan.Change
}

// fileRef is the common shape of arr webhook file resources (episodeFile,
// movieFile, and the renamed* variants).
type fileRef struct {
	Path         string `json:"path"`
	PreviousPath string `json:"previousPath"`
}

type payload struct {
	EventType string `json:"eventType"`

	Series *struct {
		Path string `json:"path"`
	} `json:"series"`
	EpisodeFile         *fileRef  `json:"episodeFile"`
	EpisodeFiles        []fileRef `json:"episodeFiles"`
	RenamedEpisodeFiles []fileRef `json:"renamedEpisodeFiles"`

	Movie *struct {
		Path       string `json:"path"`
		FolderPath string `json:"folderPath"`
	} `json:"movie"`
	MovieFile         *fileRef  `json:"movieFile"`
	MovieFiles        []fileRef `json:"movieFiles"`
	RenamedMovieFiles []fileRef `json:"renamedMovieFiles"`

	// Download/import payloads include the files replaced by an upgrade. They
	// must be reconciled as vanished paths even when a separate "file delete for
	// upgrade" notification is not enabled in arr.
	DeletedFiles []fileRef `json:"deletedFiles"`
}

// importEventTypes are the events that carry newly imported files.
var importEventTypes = map[string]bool{
	"Download":         true,
	"Import":           true,
	"Upgrade":          true,
	"DownloadComplete": true,
}

// deleteEventTypes are the events that carry a removed file path.
var deleteEventTypes = map[string]bool{
	"EpisodeFileDelete": true,
	"MovieFileDelete":   true,
}

// Parse classifies a webhook body. provider is "sonarr", "radarr", or "auto"
// (empty means auto); in auto mode the provider is inferred from the payload's
// series/movie shape. Inference failure is an error only for non-test events
// that would otherwise produce work.
func Parse(provider string, body []byte) (ParsedWebhook, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return ParsedWebhook{}, ErrMalformedPayload
	}
	eventType := strings.TrimSpace(p.EventType)
	if eventType == "" {
		return ParsedWebhook{}, ErrMalformedPayload
	}

	out := ParsedWebhook{EventType: eventType, Provider: resolveProvider(provider, p)}
	if strings.EqualFold(eventType, "Test") {
		out.Test = true
		return out, nil
	}

	switch {
	case importEventTypes[eventType]:
		out.Changes = filePaths(p, false)
		if len(out.Changes) == 0 {
			out.Changes = subtreeFallback(p)
		}
	case eventType == "Rename":
		out.Changes = filePaths(p, true)
	case deleteEventTypes[eventType]:
		out.Changes = filePaths(p, false)
	default:
		// Unsupported event type: valid delivery, nothing to do.
		return out, nil
	}

	if out.Provider == "" {
		return ParsedWebhook{}, ErrUnknownProvider
	}
	return out, nil
}

// resolveProvider returns the explicit provider when set, otherwise infers
// sonarr/radarr from the payload shape ("" when inference fails).
func resolveProvider(provider string, p payload) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderSonarr:
		return ProviderSonarr
	case ProviderRadarr:
		return ProviderRadarr
	}
	switch {
	case p.Series != nil || p.EpisodeFile != nil || len(p.EpisodeFiles) > 0 || len(p.RenamedEpisodeFiles) > 0:
		return ProviderSonarr
	case p.Movie != nil || p.MovieFile != nil || len(p.MovieFiles) > 0 || len(p.RenamedMovieFiles) > 0:
		return ProviderRadarr
	}
	return ""
}

// filePaths collects every file path in the payload as file-scope changes,
// deduped in order. includePrevious additionally collects previousPath values
// (rename events): the previous path may no longer exist, which the host's
// resolve pipeline already handles via its vanished-path fallback.
func filePaths(p payload, includePrevious bool) []autoscan.Change {
	var refs []fileRef
	if p.EpisodeFile != nil {
		refs = append(refs, *p.EpisodeFile)
	}
	refs = append(refs, p.EpisodeFiles...)
	refs = append(refs, p.RenamedEpisodeFiles...)
	if p.MovieFile != nil {
		refs = append(refs, *p.MovieFile)
	}
	refs = append(refs, p.MovieFiles...)
	refs = append(refs, p.RenamedMovieFiles...)
	refs = append(refs, p.DeletedFiles...)

	seen := make(map[string]struct{})
	var changes []autoscan.Change
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, dup := seen[path]; dup {
			return
		}
		seen[path] = struct{}{}
		changes = append(changes, autoscan.Change{SourcePath: path, Scope: autoscan.ChangeScopeFile})
	}
	for _, ref := range refs {
		add(ref.Path)
		if includePrevious {
			add(ref.PreviousPath)
		}
	}
	return changes
}

// subtreeFallback maps an import event with no file details to the series or
// movie folder as one subtree-scope change.
func subtreeFallback(p payload) []autoscan.Change {
	path := ""
	switch {
	case p.Series != nil:
		path = p.Series.Path
	case p.Movie != nil:
		path = p.Movie.FolderPath
		if strings.TrimSpace(path) == "" {
			path = p.Movie.Path
		}
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return []autoscan.Change{{SourcePath: path, Scope: autoscan.ChangeScopeSubtree}}
}
