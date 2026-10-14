package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
)

// verifyWorkerOutputToken is the core-only customer authority boundary. Workers
// receive an opaque viewer ID, never the login or API-key identity used here.
func verifyWorkerOutputToken(ctx context.Context, pool *pgxpool.Pool, content *workerContentAuthority, token, secret string) (*streamtoken.Claims, error) {
	claims, err := streamtoken.Verify(token, secret)
	if err != nil {
		return nil, errors.Join(workercontrolcore.ErrOutputRevoked, err)
	}
	if !claims.Bound() || claims.UserID <= 0 {
		return nil, workercontrolcore.ErrOutputRevoked
	}
	var active bool
	if claims.OriginLoginSessionID != "" {
		remotePlaybackAllowed := claims.DownloadArtifactID == "" && claims.PlayMethod != streamtoken.PlayMethodDownload && claims.PlayMethod != streamtoken.PlayMethodToneMapDownload && claims.ThemeID == 0
		err = pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM auth_sessions s JOIN users u ON u.id=s.user_id
			WHERE s.id=$1 AND s.user_id=$2 AND s.revoked_at IS NULL AND s.expires_at>NOW() AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at>NOW()) AND u.enabled
			AND (s.purpose!='remote_playback' OR s.bound_profile_id=$3 AND s.bound_profile_id!='' AND $4)
		)`, claims.OriginLoginSessionID, claims.UserID, claims.ProfileID, remotePlaybackAllowed).Scan(&active)
	} else {
		// apiKeyScopesAllow and compat's API-key validator admit playback and
		// downloads only for unscoped keys. A later scope edit withdraws the
		// stream just as deleting the key does; API keys have no expiry field.
		err = pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM api_keys k JOIN users u ON u.id=k.user_id
			WHERE k.id=$1 AND k.user_id=$2 AND cardinality(k.scopes)=0 AND u.enabled
		)`, claims.OriginAPIKeyID, claims.UserID).Scan(&active)
	}
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, workercontrolcore.ErrOutputRevoked
	}
	if !content.allowedClaims(ctx, claims) {
		return nil, workercontrolcore.ErrDenied
	}
	return claims, nil
}
