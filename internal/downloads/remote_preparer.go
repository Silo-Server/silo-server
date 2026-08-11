package downloads

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// NodeAwarePreparer keeps artifact queue ownership central while executing the
// expensive FFmpeg process on a healthy transcode node when capacity permits.
// Integrated installations and saturated node pools fall back to local work.
type NodeAwarePreparer struct {
	local   EncodePreparer
	planner nodepool.TranscodeWorkPlanner
	liveCfg func() *config.Config
	remote  downloadprepare.RemotePreparer
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

func (p *NodeAwarePreparer) PrepareFile(ctx context.Context, opts playback.TranscodeOpts, outputPath string) error {
	cfg := p.config()
	jwtSecret := ""
	if cfg != nil {
		jwtSecret = strings.TrimSpace(cfg.Auth.JWTSecret)
	}
	// The default artifact directory is node-local. Remote preparation is safe
	// only when the operator has deliberately configured a shared artifact path.
	if cfg == nil || jwtSecret == "" || p.remote == nil || p.planner == nil || strings.TrimSpace(cfg.Download.ArtifactDir) == "" {
		return p.local.PrepareFile(ctx, opts, outputPath)
	}
	jobID := strings.TrimSuffix(filepath.Base(outputPath), filepath.Ext(outputPath))
	node, release := p.planner.ReserveTranscodeWork("download-prepare-" + jobID)
	if node == nil {
		return p.local.PrepareFile(ctx, opts, outputPath)
	}

	slog.InfoContext(ctx, "dispatching download artifact prepare", "component", "downloads", "job_id", jobID, "node", node.URL)
	// A remote attempt gets its own staging output. If the HTTP connection fails
	// while the node is still winding down FFmpeg, a local fallback can safely
	// use outputPath.part without the two processes writing the same file.
	remoteOutputPath := outputPath + ".remote-" + uuid.NewString() + ".mp4"
	defer func() { _ = os.Remove(remoteOutputPath) }()
	err := p.remote.Prepare(ctx, node.URL, jwtSecret, downloadprepare.NewRequest(jobID, opts, remoteOutputPath))
	release()
	// PrepareFile publishes its staging output with an atomic rename, so a
	// visible file is complete even if the HTTP response was lost afterward.
	if _, statErr := os.Stat(remoteOutputPath); statErr == nil {
		if renameErr := os.Rename(remoteOutputPath, outputPath); renameErr == nil {
			return nil
		} else {
			err = renameErr
		}
	} else if err == nil {
		err = statErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	slog.WarnContext(ctx, "remote download artifact prepare unavailable; falling back to local", "component", "downloads", "job_id", jobID, "node", node.URL, "error", err)
	return p.local.PrepareFile(ctx, opts, outputPath)
}

func (p *NodeAwarePreparer) config() *config.Config {
	if p == nil || p.liveCfg == nil {
		return nil
	}
	return p.liveCfg()
}
