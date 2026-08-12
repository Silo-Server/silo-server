package downloads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// NodeAwarePreparer keeps artifact queue ownership central while executing the
// expensive FFmpeg process on a healthy transcode node when capacity permits.
// The node retains completed bytes behind an authenticated opaque-id endpoint;
// integrated installations and unavailable pools fall back to local work.
type NodeAwarePreparer struct {
	local        EncodePreparer
	planner      nodepool.TranscodeWorkPlanner
	liveCfg      func() *config.Config
	remote       downloadprepare.RemotePreparer
	originLookup artifactOriginLookup
}

type artifactOriginLookup interface {
	GetByID(ctx context.Context, id int) (*nodepool.Node, error)
}

func NewNodeAwarePreparer(local EncodePreparer, planner nodepool.TranscodeWorkPlanner, liveCfg func() *config.Config) *NodeAwarePreparer {
	if local == nil {
		local = playbackPreparer{}
	}
	return &NodeAwarePreparer{
		local:   local,
		planner: planner,
		liveCfg: liveCfg,
		remote:  downloadprepare.HTTPPreparer{},
	}
}

// SetOriginLookup supplies the authoritative node record used when the active
// pool temporarily misses an enabled node, and to recover a changed URL after
// a disabled node has left that pool.
func (p *NodeAwarePreparer) SetOriginLookup(lookup artifactOriginLookup) {
	p.originLookup = lookup
}

func (p *NodeAwarePreparer) PrepareFile(ctx context.Context, artifactID string, opts playback.TranscodeOpts, outputPath string) (PreparedArtifact, error) {
	cfg := p.config()
	jwtSecret := ""
	if cfg != nil {
		jwtSecret = strings.TrimSpace(cfg.Auth.JWTSecret)
	}
	if cfg == nil || jwtSecret == "" || p.remote == nil || p.planner == nil || !downloadprepare.ValidArtifactID(artifactID) {
		return p.local.PrepareFile(ctx, artifactID, opts, outputPath)
	}
	node, release := p.planner.ReserveTranscodeWork("download-prepare-" + artifactID)
	if node == nil {
		return p.local.PrepareFile(ctx, artifactID, opts, outputPath)
	}

	slog.InfoContext(ctx, "dispatching download artifact prepare", "component", "downloads", "artifact_id", artifactID, "node", node.URL)
	result, err := p.remote.Prepare(ctx, node.URL, jwtSecret, downloadprepare.NewRequest(artifactID, opts))
	release()
	if err == nil && result.ArtifactID == artifactID {
		return remotePreparedArtifact(node, result), nil
	}
	if err == nil {
		err = fmt.Errorf("remote download prepare returned artifact id %q, want %q", result.ArtifactID, artifactID)
	}
	if ctx.Err() != nil {
		return remotePreparedArtifact(node, downloadprepare.Result{ArtifactID: artifactID}), ctx.Err()
	}
	// A completed encode can outlive a lost HTTP response. Probe the same opaque
	// id before falling back so retry/recovery does not duplicate expensive work.
	if recovered, statErr := p.remote.Stat(ctx, node.URL, jwtSecret, artifactID); statErr == nil && recovered.ArtifactID == artifactID {
		slog.InfoContext(ctx, "recovered completed download artifact after lost response", "component", "downloads", "artifact_id", artifactID, "node", node.URL)
		return remotePreparedArtifact(node, recovered), nil
	} else if statErr == nil {
		return remotePreparedArtifact(node, downloadprepare.Result{ArtifactID: artifactID}),
			fmt.Errorf("remote download artifact recovery returned artifact id %q, want %q", recovered.ArtifactID, artifactID)
	} else if !errors.Is(statErr, downloadprepare.ErrArtifactNotFound) {
		slog.WarnContext(ctx, "remote download artifact recovery probe failed", "component", "downloads", "artifact_id", artifactID, "node", node.URL, "error", statErr)
		// The POST may have completed even though its response was lost. If the
		// follow-up probe is also indeterminate, retry the same opaque id later
		// instead of falling back locally and orphaning completed remote bytes.
		return remotePreparedArtifact(node, downloadprepare.Result{ArtifactID: artifactID}), errors.Join(err, fmt.Errorf("remote download artifact recovery probe: %w", statErr))
	}
	if ctx.Err() != nil {
		return PreparedArtifact{}, ctx.Err()
	}
	slog.WarnContext(ctx, "remote download artifact prepare unavailable; falling back to local", "component", "downloads", "artifact_id", artifactID, "node", node.URL, "error", err)
	return p.local.PrepareFile(ctx, artifactID, opts, outputPath)
}

func remotePreparedArtifact(node *nodepool.Node, result downloadprepare.Result) PreparedArtifact {
	group := ""
	if node.Group != nil {
		group = *node.Group
	}
	return PreparedArtifact{
		OriginNodeID:     node.ID,
		OriginNodeURL:    strings.TrimRight(node.URL, "/"),
		OriginNodeGroup:  group,
		OriginArtifactID: result.ArtifactID,
		FileSize:         result.FileSize,
	}
}

func (p *NodeAwarePreparer) ResolveArtifact(ctx context.Context, artifact *Artifact) error {
	if artifact == nil || artifact.OriginNodeID == 0 || p.planner == nil {
		return ErrArtifactOriginRemoved
	}
	node, ok := p.planner.TranscodeNode(artifact.OriginNodeID)
	if !ok || node == nil {
		if p.originLookup != nil {
			configured, err := p.originLookup.GetByID(ctx, artifact.OriginNodeID)
			switch {
			case err == nil && configured != nil && configured.Type == nodepool.NodeTypeTranscode:
				applyArtifactOrigin(artifact, configured)
				if configured.Enabled {
					return nil
				}
			case err != nil && !errors.Is(err, nodepool.ErrNodeNotFound):
				return fmt.Errorf("looking up artifact origin node: %w", err)
			}
		}
		return ErrArtifactOriginRemoved
	}
	applyArtifactOrigin(artifact, node)
	return nil
}

func applyArtifactOrigin(artifact *Artifact, node *nodepool.Node) {
	artifact.OriginNodeURL = strings.TrimRight(node.URL, "/")
	artifact.OriginNodeGroup = ""
	if node.Group != nil {
		artifact.OriginNodeGroup = *node.Group
	}
}

func (p *NodeAwarePreparer) StatArtifact(ctx context.Context, artifact *Artifact) (downloadprepare.Result, error) {
	if err := p.ResolveArtifact(ctx, artifact); err != nil {
		return downloadprepare.Result{}, err
	}
	secret, err := p.remoteCredentials(artifact)
	if err != nil {
		return downloadprepare.Result{}, err
	}
	return p.remote.Stat(ctx, artifact.OriginNodeURL, secret, artifact.OriginArtifactID)
}

func (p *NodeAwarePreparer) DeleteArtifact(ctx context.Context, artifact *Artifact) error {
	// Prefer the authoritative current URL, including for a disabled node. A
	// deleted node has no newer record, so retain the last persisted URL as the
	// best-effort cleanup target.
	_ = p.ResolveArtifact(ctx, artifact)
	secret, err := p.remoteCredentials(artifact)
	if err != nil {
		return err
	}
	return p.remote.Delete(ctx, artifact.OriginNodeURL, secret, artifact.OriginArtifactID)
}

func (p *NodeAwarePreparer) remoteCredentials(artifact *Artifact) (string, error) {
	if artifact == nil || strings.TrimSpace(artifact.OriginNodeURL) == "" || !downloadprepare.ValidArtifactID(artifact.OriginArtifactID) {
		return "", errors.New("remote artifact locator is incomplete")
	}
	cfg := p.config()
	if cfg == nil || strings.TrimSpace(cfg.Auth.JWTSecret) == "" || p.remote == nil {
		return "", errors.New("remote artifact credentials are unavailable")
	}
	return strings.TrimSpace(cfg.Auth.JWTSecret), nil
}

func (p *NodeAwarePreparer) config() *config.Config {
	if p == nil || p.liveCfg == nil {
		return nil
	}
	return p.liveCfg()
}
