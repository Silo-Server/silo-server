package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

const workerTranscodeMode = "transcode"

// workerOutputRelay supplies private viewer credentials only after the output
// transport has identified an enrolled worker. Public tokenless media URLs and
// legacy node requests do not change.
type workerOutputRelay struct {
	pool        *pgxpool.Pool
	recipes     workercontrolcore.RecipeStore
	authorities workercontrolcore.OutputAuthorities
	assignments workercontrolcore.AssignmentStore
	secret      func() string
	now         func() time.Time
}

//nolint:nestif // Retained grants must pass the complete credential, artifact, generation and epoch checks before reuse.
func (r workerOutputRelay) token(request *http.Request, node workercontrolcore.Node) (string, error) {
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	if r.secret == nil {
		return "", workercontrolcore.ErrUnavailable
	}
	secret := r.secret()
	if secret == "" {
		return "", workercontrolcore.ErrUnavailable
	}
	if len(parts) >= 2 && (parts[0] == workerTranscodeMode || parts[0] == string(playback.PlayRemux)) {
		if r.recipes == nil {
			return "", workercontrolcore.ErrUnavailable
		}
		card, ok := r.recipes.Get(request.Context(), parts[1])
		if !ok || card == nil || !card.Bound() || card.OriginalStartedAt.IsZero() {
			return "", workercontrolcore.ErrDenied
		}
		claims := card.ToClaims()
		if workercontrolcore.ExtractOutputWork(&claims) != parts[1] || !workeridentity.SameOrigin(claims.TranscodeNode, node.URL, node.AllowHTTP) || claims.RoutingExecutionNodeID != 0 && claims.RoutingExecutionNodeID != node.ID {
			return "", workercontrolcore.ErrDenied
		}
		// Anchor each full-day window to the original play. Stable recipes
		// share one JWT across segments; a live header-authenticated play can
		// renew past its first day without borrowing the current caller's login.
		// JWT NumericDate uses whole seconds; rotate on that same boundary.
		issued := time.Unix(card.OriginalStartedAt.Unix(), 0)
		if elapsed := now.Sub(issued); elapsed >= playback.MaxTokenTTL {
			issued = issued.Add(elapsed.Truncate(playback.MaxTokenTTL))
		}
		return streamtoken.SignAt(claims, secret, issued, playback.MaxTokenTTL)
	}
	if len(parts) == 3 && parts[0] == "downloads" && parts[1] == "artifacts" {
		return r.downloadToken(request, node, parts[2], secret, now)
	}
	return "", workercontrolcore.ErrDenied
}

//nolint:gocyclo,nestif // Retained grants must pass the complete credential, artifact, generation and epoch checks before reuse.
func (r workerOutputRelay) downloadToken(request *http.Request, node workercontrolcore.Node, work, secret string, now time.Time) (string, error) {
	caller := apimw.GetClaims(request.Context())
	profile := apimw.GetProfileID(request.Context())
	if caller == nil || caller.UserID <= 0 || profile == "" || r.pool == nil || r.authorities == nil || r.assignments == nil {
		return "", workercontrolcore.ErrDenied
	}
	var origin streamtoken.OriginCredential
	switch caller.TokenType {
	case auth.TokenTypeAccess, auth.TokenTypeDirectDownloadLink:
		origin.OriginLoginSessionID = caller.SessionID
	case auth.TokenTypeAPIKey:
		origin.OriginAPIKeyID = caller.APIKeyID
	}
	if !origin.Bound() {
		return "", workercontrolcore.ErrDenied
	}
	rows, err := r.pool.Query(request.Context(), "SELECT id FROM download_artifacts WHERE origin_node_id=$1 AND origin_artifact_id=$2 LIMIT 2", node.ID, work)
	if err != nil {
		return "", err
	}
	var rowID string
	for rows.Next() {
		if rowID != "" {
			rows.Close()
			return "", workercontrolcore.ErrDenied
		}
		if err := rows.Scan(&rowID); err != nil {
			rows.Close()
			return "", err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if rowID == "" {
		return "", workercontrolcore.ErrDenied
	}
	artifact, err := downloads.NewArtifactRepository(r.pool).GetByID(request.Context(), rowID)
	if err != nil {
		return "", err
	}
	if caller.TokenType == auth.TokenTypeDirectDownloadLink && caller.FileID != artifact.MediaFileID {
		return "", workercontrolcore.ErrDenied
	}
	if !workeridentity.SameOrigin(artifact.OriginNodeURL, node.URL, node.AllowHTTP) {
		return "", workercontrolcore.ErrDenied
	}
	identity, err := json.Marshal([]any{origin, caller.UserID, profile, rowID})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(identity)
	claims := streamtoken.Claims{OriginCredential: origin, SessionID: "download-" + hex.EncodeToString(digest[:16]), UserID: caller.UserID, ProfileID: profile, MediaFileID: artifact.MediaFileID, PlayMethod: streamtoken.PlayMethodDownload, TranscodeNode: node.URL, RoutingExecutionNodeID: node.ID, DownloadArtifactID: work, DownloadArtifactRowID: rowID, DownloadArtifactSize: artifact.FileSize}
	if artifact.ToneMapMode != "" || artifact.AudioRecipeVersion != "" || artifact.TrackRecipeVersion != "" {
		claims.PlayMethod = streamtoken.PlayMethodToneMapDownload
		claims.DownloadExecutionFingerprint = artifact.ParamsHash
		if artifact.FileSize <= 0 {
			return "", workercontrolcore.ErrDenied
		}
	}
	if !workerPreparedArtifactMatches(artifact, &claims) {
		return "", workercontrolcore.ErrDenied
	}
	assignment, found, err := r.assignments.Get(request.Context(), node.ID, work)
	if err != nil {
		return "", err
	}
	if !found {
		return "", workercontrolcore.ErrDenied
	}
	epoch := workeridentity.OutputEpoch(secret)
	lookupKey := workercontrolcore.OutputAuthorityLookupKey(claims, node.ID, work, assignment.Generation, epoch)
	if lookupKey == "" {
		return "", workercontrolcore.ErrDenied
	}
	if finder, ok := r.authorities.(workercontrolcore.OutputAuthorityFinder); ok {
		record, found, err := finder.Find(request.Context(), node.ID, lookupKey)
		if err != nil {
			return "", err
		}
		if found {
			if !record.ValidForLookup(node.ID, lookupKey, now) || record.WorkID != work || record.Generation != assignment.Generation || record.Epoch != epoch {
				return "", workercontrolcore.ErrDenied
			}
			if record.ExpiresAt > now.Add(5*time.Minute).Unix() {
				retained, err := streamtoken.Verify(record.Token, secret)
				if err != nil || retained.ExpiresAt == nil || retained.ExpiresAt.Unix() != record.ExpiresAt || retained.IssuedAt == nil || retained.IssuedAt.After(now) || workercontrolcore.OutputAuthorityLookupKey(*retained, node.ID, work, assignment.Generation, epoch) != lookupKey {
					return "", workercontrolcore.ErrDenied
				}
				return record.Token, nil
			}
		}
	}
	// Whole-file relays reuse the exact source/row/work/generation token from
	// the bounded registry, rather than minting one for every Range request.
	// IssueOutputGrant still rechecks its live source and content authority.
	return streamtoken.SignAt(claims, secret, now, playback.MaxTokenTTL)
}
