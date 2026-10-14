package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const workerPreparePath = "/downloads/prepare"

// Pin the durable attempt before worker execution. Reusing the same core
// lease-owner string does not admit an earlier retry: attempts must also match.
func admitWorkerDownload(ctx context.Context, pool *pgxpool.Pool, node workercontrolcore.Node, a workerwire.Assignment) error {
	if a.Path != workerPreparePath {
		return nil
	}
	var request downloadprepare.Request
	if err := json.Unmarshal(a.Body, &request); err != nil {
		return err
	}
	artifact := strings.TrimPrefix(request.LogSessionID, "download-prepare-")
	if artifact == request.LogSessionID || artifact == "" || !strings.HasPrefix(a.WorkID, artifact+"-") {
		return errors.New("download attempt has no durable job authority")
	}
	tag, err := pool.Exec(ctx, `INSERT INTO worker_http_download_attempts(node_id,work_id,generation,artifact_id,lease_owner,attempt)
		SELECT $1,$2,$3,id,lease_owner,attempts FROM download_artifacts
		WHERE id=$4 AND worker_node_id=$1 AND lease_owner IS NOT NULL AND lease_expires_at>now()
		AND status IN ('running','tone_map_running','audio_v2_running','tracks_v1_running')
		ON CONFLICT(node_id,work_id,generation) DO NOTHING`, node.ID, a.WorkID, a.Generation, artifact)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("download attempt lease unavailable")
	}
	return nil
}

func workerDownloadActive(ctx context.Context, pool *pgxpool.Pool, node int, a workerwire.Assignment) bool {
	active, err := workerDownloadStatus(ctx, pool, node, a)
	return err == nil && active
}

func workerDownloadStatus(ctx context.Context, pool *pgxpool.Pool, node int, a workerwire.Assignment) (bool, error) {
	var active bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_http_download_attempts p JOIN download_artifacts a ON a.id=p.artifact_id
		WHERE p.node_id=$1 AND p.work_id=$2 AND p.generation=$3 AND (
		(a.worker_node_id=$1 AND a.attempts=p.attempt AND a.lease_owner=p.lease_owner AND a.lease_expires_at>now()
		AND a.status IN ('running','tone_map_running','audio_v2_running','tracks_v1_running')) OR
		(a.origin_node_id=$1 AND a.origin_artifact_id=$2 AND a.attempts=p.attempt
		AND a.status IN ('ready','tone_map_ready','audio_v2_ready','tracks_v1_ready'))))`, node, a.WorkID, a.Generation).Scan(&active)
	return active, err
}
