package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
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
// a renewed grant. A node calls it once when a job's input opens and again
// every renewal, so catalog state is never more than one renewal stale.
//
// 403: the grant does not verify, has expired, or is another node's. 410: the
// catalog no longer has the source at the granted path, or it is no longer a
// regular file. 412: the file's size or modification time changed.
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
	if status := s.catalogStatus(r.Context(), claims); status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if _, err := claims.Check(); err != nil {
		status := sourceStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	renewed, err := s.opts.Grants.Renew(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "renew source grant", "component", "nodecontrol", "node_id", id, "error", err)
		http.Error(w, "source grants unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(nodesource.OpenResponse{
		Grant: renewed,
		Path:  claims.Path,
		Size:  claims.Size,
		Mtime: claims.Mtime,
		ETag:  claims.ETag(),
	})
}

// catalogStatus is 0 when the catalog still has the source claims name at
// its path, 410 when it does not, and 503 when the catalog cannot say. It
// reads only the catalog: a theme's row records no revision, so the caller
// checks the file itself (Grant.Check at open, the stream token's theme
// revision at resolve).
func (s *Server) catalogStatus(ctx context.Context, claims nodesource.Grant) int {
	var (
		active bool
		err    error
	)
	switch claims.Kind {
	case nodesource.KindFile:
		if s.opts.Files == nil {
			return http.StatusServiceUnavailable
		}
		active, err = s.opts.Files.IsActiveFile(ctx, int(claims.ID), claims.Path)
	case nodesource.KindTheme:
		if s.opts.Themes == nil {
			return http.StatusServiceUnavailable
		}
		active, err = s.opts.Themes.IsActiveTheme(ctx, claims.ID, claims.Path)
	}
	if err != nil {
		slog.WarnContext(ctx, "check granted source against the catalog", "component", "nodecontrol",
			"kind", claims.Kind, "id", claims.ID, "error", err)
		return http.StatusServiceUnavailable
	}
	if !active {
		return http.StatusGone
	}
	return 0
}

// HandleSource serves GET and HEAD /api/internal/nodes/sources: the bytes of
// the source granted by the nodesource.HeaderSourceGrant header, to the node
// the grant names. The grant is a header so no access log on the way records
// it. It reads no catalog: open checked it, and checks it again at every
// renewal. The granted path is opened once and checked against the grant's
// revision, and the bytes come from that descriptor (playback.ServeOpenFile),
// so a file replaced after the check is never served. Single and suffix
// ranges are supported; a conditional request can only narrow the answer to
// the grant's revision, never widen or redirect it.
//
// 403: the grant does not verify, has expired, or is another node's. 410:
// the file is gone. 412: its size or modification time changed.
func (s *Server) HandleSource(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, status := s.verifyGrant(r.Context(), id, strings.TrimSpace(r.Header.Get(nodesource.HeaderSourceGrant)))
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	f, info, err := claims.Open()
	if err != nil {
		status := sourceStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer func() { _ = f.Close() }()
	if s.sourceOpened != nil {
		s.sourceOpened()
	}
	// If-Match is the only condition a node may set, and it can only fail
	// the request. The others could turn a range into a whole body
	// (If-Range) or a body into nothing (If-None-Match, If-Modified-Since).
	for _, name := range ignoredSourceConditions {
		r.Header.Del(name)
	}
	w.Header().Set("Cache-Control", "no-store")
	playback.ServeOpenFile(w, r, f, info, claims.ETag())
}

// SubtitleBundles builds and caches the subtitle bundles a text burn-in of a
// granted source needs. *playback.SubtitleCache implements it.
type SubtitleBundles interface {
	OpenSubtitleBundle(ctx context.Context, src playback.SubtitleBundleSource) (*os.File, error)
}

// subtitleBundleWait is how long HandleSubtitle waits for a bundle being built
// before it answers 202. It stays under the node's wait for response headers.
const subtitleBundleWait = 5 * time.Second

// HandleSubtitle serves GET /api/internal/nodes/sources/subtitle: the
// subtitle bundle (playback.SubtitleBundleSource) of one subtitle track of the
// source granted by the nodesource.HeaderSourceGrant header, so a node that
// reads the source over HTTP can burn the track in without demuxing the whole
// source through the relay. The core builds the bundle from its own copy and
// caches it; a request waits a few seconds for a build and otherwise answers
// 202, and the node asks again. Like HandleSource it reads no catalog.
//
// 400: the track or codec is missing or malformed, or the grant is not a
// media file's. 403: the grant does not verify, has expired, or is another
// node's. 410: the file is gone. 412: its size or modification time changed.
// 422: the source cannot give the bundle (playback.ErrSubtitleBundleFailed).
// 503: no bundle can be built now.
func (s *Server) HandleSubtitle(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, status := s.verifyGrant(r.Context(), id, strings.TrimSpace(r.Header.Get(nodesource.HeaderSourceGrant)))
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	query := r.URL.Query()
	track, err := strconv.Atoi(query.Get(nodesource.QuerySubtitleTrack))
	codec := strings.ToLower(query.Get(nodesource.QuerySubtitleCodec))
	if err != nil || track < 0 || !validSubtitleCodec(codec) || claims.Kind != nodesource.KindFile {
		http.Error(w, "invalid subtitle request", http.StatusBadRequest)
		return
	}
	if s.opts.Subtitles == nil {
		http.Error(w, "subtitle bundles unavailable", http.StatusServiceUnavailable)
		return
	}
	// The bundle is built from the granted path, after the same revision
	// check an open makes; the subtitle cache keys it by that revision.
	if _, err := claims.Check(); err != nil {
		status := sourceStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	ffmpeg := ""
	if s.opts.FFmpegPath != nil {
		ffmpeg = s.opts.FFmpegPath()
	}
	ctx, cancel := context.WithTimeout(r.Context(), subtitleBundleWait)
	defer cancel()
	bundle, err := s.opts.Subtitles.OpenSubtitleBundle(ctx, playback.SubtitleBundleSource{
		Path: claims.Path, Track: track, Codec: codec, FFmpegPath: ffmpeg,
	})
	switch {
	case errors.Is(err, playback.ErrSubtitleBundlePending):
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
		return
	case errors.Is(err, playback.ErrSubtitleBundleFailed):
		slog.WarnContext(r.Context(), "subtitle bundle cannot be built", "component", "nodecontrol",
			"node_id", id, "media_file_id", claims.ID, "track", track, "error", err)
		http.Error(w, "the subtitle track cannot be staged", http.StatusUnprocessableEntity)
		return
	case err != nil:
		slog.WarnContext(r.Context(), "subtitle bundle unavailable", "component", "nodecontrol",
			"node_id", id, "media_file_id", claims.ID, "track", track, "error", err)
		http.Error(w, "subtitle bundle unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = bundle.Close() }()
	info, err := bundle.Stat()
	if err != nil {
		http.Error(w, "subtitle bundle unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", info.ModTime(), bundle)
}

// validSubtitleCodec reports a codec name as FFmpeg spells one.
func validSubtitleCodec(codec string) bool {
	if codec == "" || len(codec) > 32 {
		return false
	}
	for _, c := range codec {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// ignoredSourceConditions are the conditional request headers a source read
// drops; see HandleSource.
var ignoredSourceConditions = []string{headerIfNoneMatch, "If-Modified-Since", "If-Unmodified-Since", "If-Range"}

// verifyGrant checks token and that it names node nodeID. It returns the
// claims, or the status to refuse with: 403 for a grant that does not verify
// or is another node's, 503 for a core without source grants.
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

// sourceStatus is the status a failed source check answers with.
func sourceStatus(err error) int {
	if errors.Is(err, nodesource.ErrSourceChanged) {
		return http.StatusPreconditionFailed
	}
	return http.StatusGone
}

// grantResolution mints the source grant a resolution's recipe runs on, for
// node nodeID. Like open, it first checks that the catalog still has the
// recipe's exact file, or its theme in an enabled library: a recipe outlives
// the catalog row it was made from, and a grant must never be minted for a
// file the catalog removed or moved.
func (s *Server) grantResolution(ctx context.Context, nodeID int, resolution *Resolution) *resolveRefusal {
	if s.opts.Grants == nil {
		return refuseUnavailable
	}
	card := resolution.Recipe
	source := nodesource.Grant{Kind: nodesource.KindFile, ID: int64(card.MediaFileID), Path: card.InputPath}
	if resolution.Theme != nil {
		source.Kind, source.ID = nodesource.KindTheme, resolution.Theme.ID
	}
	switch s.catalogStatus(ctx, source) {
	case 0:
	case http.StatusGone:
		slog.WarnContext(ctx, "resolve: recipe source left the catalog", "component", "nodecontrol", "node_id", nodeID,
			"kind", source.Kind, "id", source.ID)
		return refuseSourceChanged
	default:
		return refuseUnavailable
	}
	var (
		grant string
		err   error
	)
	if resolution.Theme != nil {
		grant, err = s.opts.Grants.ForTheme(ctx, nodeID, resolution.Theme.ID, card.InputPath)
	} else {
		grant, err = s.opts.Grants.ForFile(ctx, nodeID, &models.MediaFile{ID: card.MediaFileID, FilePath: card.InputPath})
	}
	switch {
	case err == nil:
		resolution.SourceGrant = grant
		return nil
	case errors.Is(err, nodesource.ErrSourceGone), errors.Is(err, nodesource.ErrInvalidGrant):
		slog.WarnContext(ctx, "resolve: recipe source unavailable", "component", "nodecontrol", "node_id", nodeID,
			"media_file_id", card.MediaFileID, "error", err)
		return refuseSourceChanged
	default:
		slog.ErrorContext(ctx, "resolve: mint source grant", "component", "nodecontrol", "node_id", nodeID, "error", err)
		return refuseUnavailable
	}
}
