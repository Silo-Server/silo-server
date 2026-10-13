package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

func ExtractOutputWork(claims *streamtoken.Claims) string {
	if claims == nil {
		return ""
	}
	if claims.DownloadArtifactID != "" {
		return claims.DownloadArtifactID
	}
	return tokenWork(claims)
}

// IssueOutputGrant verifies the existing customer token on the core, then
// delegates output request proofs to one worker/work/generation public key.
func (s *Service) IssueOutputGrant(ctx context.Context, token, nodeURL, proxySecret string) (GrantReply, error) {
	if s.options.VerifyToken == nil || proxySecret == "" {
		return GrantReply{}, ErrDenied
	}
	claims, err := s.options.VerifyToken(ctx, token)
	if err != nil || claims == nil {
		return GrantReply{}, ErrDenied
	}
	node, found, err := s.options.Nodes.ByURL(ctx, nodeURL)
	if err != nil || !found || !node.Enabled || strings.TrimRight(claims.TranscodeNode, "/") != node.URL || claims.RoutingExecutionNodeID != 0 && claims.RoutingExecutionNodeID != node.ID {
		return GrantReply{}, ErrDenied
	}
	if canonical, e := workeridentity.ValidateOrigin(node.URL, node.AllowHTTP); e != nil || canonical != node.URL {
		return GrantReply{}, ErrDenied
	}
	work := ExtractOutputWork(claims)
	if !validWork(work) {
		return GrantReply{}, ErrDenied
	}
	copied := *claims
	var generation uint64
	var artifactFingerprint, clientFingerprint string
	if claims.DownloadArtifactID != "" {
		a, ok, e := s.options.Store.Get(ctx, node.ID, work)
		if e != nil || !ok || !s.WorkActive(ctx, node.ID, work, a.Generation) {
			return GrantReply{}, ErrDenied
		}
		generation = a.Generation
		copied.MediaPath = ""
		binder, ok := s.options.Store.(InputBinder)
		if !ok {
			return GrantReply{}, ErrUnavailable
		}
		bindings, e := binder.InputBindings(ctx, node.ID, work, generation)
		if e != nil {
			return GrantReply{}, e
		}
		artifactFingerprint = bindings[receiptWorker]
		clientFingerprint = bindings[receiptOriginal]
		if artifactFingerprint == "" || clientFingerprint == "" || claims.DownloadExecutionFingerprint != "" && claims.DownloadExecutionFingerprint != clientFingerprint {
			return GrantReply{}, ErrDenied
		}
	} else {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.options.Origin, nil)
		reply, e := s.currentRecipe(req, node, work)
		if e != nil {
			return GrantReply{}, e
		}
		card := reply.Recipe
		if card.UserID != 0 && card.UserID != claims.UserID || card.ProfileID != "" && card.ProfileID != claims.ProfileID || card.MediaFileID != 0 && card.MediaFileID != claims.MediaFileID {
			return GrantReply{}, ErrDenied
		}
		generation = reply.Assignment.Generation
		copied.MediaPath = card.InputPath
		copied.OutputSubdir = ""
	}
	expires := time.Now().Add(5 * time.Minute)
	if claims.ExpiresAt == nil || !time.Now().Before(claims.ExpiresAt.Time) {
		return GrantReply{}, ErrDenied
	}
	if claims.ExpiresAt.Before(expires) {
		expires = claims.ExpiresAt.Time
	}
	grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: node.URL, NodeID: node.ID, WorkID: work, Generation: generation, ExpiresAt: expires.Unix()}
	private, err := workeridentity.OutputPrivateKey(proxySecret, grant)
	if err != nil {
		return GrantReply{}, err
	}
	inner, err := json.Marshal(copied)
	if err != nil {
		return GrantReply{}, err
	}
	public := ed25519.PublicKey(append([]byte(nil), private[ed25519.SeedSize:]...))
	authorityID := ""
	if claims.DownloadArtifactID != "" {
		if s.options.OutputAuthorities == nil {
			return GrantReply{}, ErrUnavailable
		}
		authorityID, err = s.options.OutputAuthorities.Register(ctx, OutputAuthorityRecord{Token: token, NodeID: node.ID, WorkID: work, Generation: generation, Epoch: workeridentity.OutputEpoch(proxySecret), ExpiresAt: claims.ExpiresAt.Unix()})
		if err != nil {
			return GrantReply{}, err
		}
	}
	grant.Body, err = json.Marshal(workeridentity.OutputAuthority{AuthorityID: authorityID, ProxyPublicKey: public, Claims: inner, Epoch: workeridentity.OutputEpoch(proxySecret), ArtifactFingerprint: artifactFingerprint, ClientFingerprint: clientFingerprint})
	if err != nil {
		return GrantReply{}, err
	}
	encoded, err := workeridentity.EncodeGrant(grant, s.options.PrivateKey)
	return GrantReply{Grant: encoded}, err
}
