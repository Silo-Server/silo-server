package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// GrantIssuer mints, verifies and renews source grants. *nodesource.Grants
// implements it.
type GrantIssuer interface {
	ForFile(ctx context.Context, nodeID int, file *models.MediaFile) (string, error)
	ForTheme(ctx context.Context, nodeID int, themeID int64, path string) (string, error)
	Verify(token string) (nodesource.Grant, error)
	Renew(claims nodesource.Grant) (string, error)
}

// FileCatalog checks a granted media file against the catalog.
// *scanner.FileRepository implements it.
type FileCatalog interface {
	// IsActiveFile reports whether media file id is still in the catalog,
	// not missing, at exactly path.
	IsActiveFile(ctx context.Context, id int, path string) (bool, error)
}

// ThemeCatalog checks a granted theme file against the catalog.
// *themesongs.Repository implements it.
type ThemeCatalog interface {
	// IsActiveTheme reports whether theme id still names exactly path in an
	// enabled library.
	IsActiveTheme(ctx context.Context, id int64, path string) (bool, error)
}

// HandleSourceOpen serves POST /api/internal/nodes/self/sources/open: it
// exchanges a source grant the calling node holds for the approved source and
// a renewed grant with the same revision. A node calls it once when a job's
// input opens and again at every renewal, so while renewals succeed its
// catalog check is at most one renewal interval old, and up to one grant
// lifetime old when they fail transiently.
//
// 403: the grant does not verify, has expired, or is another node's. 410: the
// catalog no longer has the source at the granted path, or it is gone or no
// longer a regular file. 412: the file's size or modification time changed.
// 503: no grant key, or the catalog cannot be read.
func (s *Server) HandleSourceOpen(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req nodesource.OpenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes)).Decode(&req); err != nil || req.Grant == "" {
		http.Error(w, "invalid open request", http.StatusBadRequest)
		return
	}
	claims, status := s.verifyGrant(r.Context(), id, req.Grant)
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if status := s.catalogStatus(r.Context(), claims.Kind, claims.ID, claims.Path); status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if _, err := claims.Check(r.Context()); err != nil {
		status := sourceStatus(err)
		if r.Context().Err() != nil {
			status = http.StatusServiceUnavailable
		}
		if status == http.StatusServiceUnavailable {
			slog.WarnContext(r.Context(), "source grant check could not examine the file", "component", "nodecontrol",
				"node_id", id, "kind", claims.Kind, "id", claims.ID, "error", err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	renewed, err := s.opts.Grants.Renew(claims)
	if errors.Is(err, nodesource.ErrGrantExpired) {
		// It reached MaxGrantAge between Verify and Renew.
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "renew source grant", "component", "nodecontrol", "node_id", id, "error", err)
		http.Error(w, "source grants unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(nodesource.OpenResponse{
		Grant:           renewed,
		Path:            claims.Path,
		Size:            claims.Size,
		ModTimeUnixNano: claims.ModTimeUnixNano,
		ETag:            claims.ETag(),
	}); err != nil {
		slog.DebugContext(r.Context(), "write source open answer", "component", "nodecontrol", "node_id", id, "error", err)
	}
}

// catalogStatus is 0 when the catalog still has the source at path, 410 when
// it does not, and 503 when the catalog cannot say. A media file must be the
// row id, not missing, at exactly path; a theme must be id at exactly path in
// an enabled library. It reads only the catalog: the caller checks the file.
func (s *Server) catalogStatus(ctx context.Context, kind string, id int64, path string) int {
	var (
		active bool
		err    error
	)
	switch kind {
	case nodesource.KindFile:
		if s.opts.Files == nil {
			return http.StatusServiceUnavailable
		}
		active, err = s.opts.Files.IsActiveFile(ctx, int(id), path)
	case nodesource.KindTheme:
		if s.opts.Themes == nil {
			return http.StatusServiceUnavailable
		}
		active, err = s.opts.Themes.IsActiveTheme(ctx, id, path)
	default:
		return http.StatusGone
	}
	if err != nil {
		slog.WarnContext(ctx, "check granted source against the catalog", "component", "nodecontrol",
			"kind", kind, "id", id, "error", err)
		return http.StatusServiceUnavailable
	}
	if !active {
		return http.StatusGone
	}
	return 0
}

// verifyGrant checks token and that it names node nodeID. It returns the
// claims, or the status to refuse with: 403 for a grant that does not verify,
// has expired, or is another node's; 503 for a core without source grants.
func (s *Server) verifyGrant(ctx context.Context, nodeID int, token string) (nodesource.Grant, int) {
	if s.opts.Grants == nil {
		return nodesource.Grant{}, http.StatusServiceUnavailable
	}
	claims, err := s.opts.Grants.Verify(token)
	if errors.Is(err, nodesource.ErrNoGrantKey) {
		return nodesource.Grant{}, http.StatusServiceUnavailable
	}
	if err != nil {
		return nodesource.Grant{}, http.StatusForbidden
	}
	if claims.Node != nodeID {
		slog.WarnContext(ctx, "source grant presented by another node", "component", "nodecontrol",
			"node_id", nodeID, "grant_node_id", claims.Node)
		return nodesource.Grant{}, http.StatusForbidden
	}
	return claims, 0
}

// sourceStatus is the status a failed source check answers with. A node ends
// its job on 410 and 412, so they are kept for a file that is gone or
// changed. A file that could not be examined answers 503, which the node
// retries.
func sourceStatus(err error) int {
	switch {
	case errors.Is(err, nodesource.ErrSourceChanged):
		return http.StatusPreconditionFailed
	case errors.Is(err, nodesource.ErrSourceGone):
		return http.StatusGone
	}
	return http.StatusServiceUnavailable
}

// grantResolution checks that the catalog still has the source of a settled
// resolution, and mints the grant the calling node opens it with. A recipe
// outlives the catalog row it was made from, so the check is as strict as the
// node's own input approval: the recipe's exact media file (by id, not
// missing, at exactly the recipe's path, a regular file), or its theme in an
// enabled library. A source that fails it is ResolveSourceChanged.
func (s *Server) grantResolution(ctx context.Context, nodeID int, resolution *Resolution) *resolveRefusal {
	if s.opts.Grants == nil {
		return refuseUnavailable
	}
	card := resolution.Recipe
	kind, id := nodesource.KindFile, int64(card.MediaFileID)
	if resolution.Theme != nil {
		kind, id = nodesource.KindTheme, resolution.Theme.ID
	}
	if id <= 0 {
		slog.WarnContext(ctx, "resolve: recipe names no catalog source", "component", "nodecontrol", "node_id", nodeID, "kind", kind)
		return refuseSourceChanged
	}
	switch s.catalogStatus(ctx, kind, id, card.InputPath) {
	case 0:
	case http.StatusGone:
		slog.WarnContext(ctx, "resolve: recipe source left the catalog", "component", "nodecontrol", "node_id", nodeID,
			"kind", kind, "id", id)
		return refuseSourceChanged
	default:
		return refuseUnavailable
	}
	var (
		grant string
		err   error
	)
	if kind == nodesource.KindTheme {
		grant, err = s.opts.Grants.ForTheme(ctx, nodeID, id, card.InputPath)
	} else {
		grant, err = s.opts.Grants.ForFile(ctx, nodeID, &models.MediaFile{ID: card.MediaFileID, FilePath: card.InputPath})
	}
	switch {
	case err == nil:
		resolution.SourceGrant = grant
		return nil
	case ctx.Err() != nil:
		return refuseUnavailable
	case errors.Is(err, nodesource.ErrSourceGone), errors.Is(err, nodesource.ErrInvalidGrant):
		slog.WarnContext(ctx, "resolve: recipe source unavailable", "component", "nodecontrol", "node_id", nodeID,
			"kind", kind, "id", id, "error", err)
		return refuseSourceChanged
	default:
		slog.ErrorContext(ctx, "resolve: mint source grant", "component", "nodecontrol", "node_id", nodeID, "error", err)
		return refuseUnavailable
	}
}
