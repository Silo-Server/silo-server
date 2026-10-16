package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
)

type viewerCredentialProbe struct {
	login          string
	key            int64
	user           int
	profile        string
	remotePlayback bool
}

type viewerContentDecision struct {
	allowed bool
	err     error
}

// The cache belongs to one verification call. Retain every content-sensitive
// claim field, including prepared/theme identity, policy and source path inputs.
func viewerContentKey(claims streamtoken.Claims) (string, error) {
	claims.OriginCredential = streamtoken.OriginCredential{}
	claims.IssuedAt, claims.ExpiresAt = nil, nil
	claims.SessionID, claims.TranscodeTransportID = "", ""
	encoded, err := json.Marshal(claims)
	return string(encoded), err
}

func verifyWorkerOutputTokens(ctx context.Context, pool *pgxpool.Pool, content *workerContentAuthority, tokens []string, secret string) ([]workercontrolcore.TokenVerification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(tokens) > workercontrolcore.MaxOutputVerificationTokens {
		return nil, workercontrolcore.ErrUnavailable
	}
	results := make([]workercontrolcore.TokenVerification, len(tokens))
	probes := make([]viewerCredentialProbe, len(tokens))
	logins, keys := map[viewerCredentialProbe]bool{}, map[viewerCredentialProbe]bool{}
	for i, token := range tokens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		claims, err := streamtoken.Verify(token, secret)
		if err != nil {
			results[i].Err = errors.Join(workercontrolcore.ErrOutputRevoked, err)
			continue
		}
		if !claims.Bound() || claims.UserID <= 0 || claims.UserID > math.MaxInt32 {
			results[i].Err = workercontrolcore.ErrOutputRevoked
			continue
		}
		results[i].Claims = claims
		probe := viewerCredentialProbe{login: claims.OriginLoginSessionID, key: claims.OriginAPIKeyID, user: claims.UserID, profile: claims.ProfileID, remotePlayback: claims.DownloadArtifactID == "" && claims.PlayMethod != streamtoken.PlayMethodDownload && claims.PlayMethod != streamtoken.PlayMethodToneMapDownload && claims.ThemeID == 0}
		probes[i] = probe
		if probe.login != "" {
			logins[probe] = false
		} else {
			keys[probe] = false
		}
	}
	if len(logins)+len(keys) > 0 && pool == nil {
		return nil, workercontrolcore.ErrUnavailable
	}
	if err := verifyViewerLoginProbes(ctx, pool, logins); err != nil {
		return nil, err
	}
	if err := verifyViewerKeyProbes(ctx, pool, keys); err != nil {
		return nil, err
	}
	decisions := make(map[string]viewerContentDecision)
	for i := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if results[i].Err != nil {
			continue
		}
		probe := probes[i]
		active := keys[probe]
		if probe.login != "" {
			active = logins[probe]
		}
		if !active {
			results[i].Claims = nil
			results[i].Err = workercontrolcore.ErrOutputRevoked
			continue
		}
		key, err := viewerContentKey(*results[i].Claims)
		if err != nil {
			return nil, err
		}
		decision, found := decisions[key]
		if !found {
			decision.allowed, decision.err = content.checkClaims(ctx, results[i].Claims)
			decisions[key] = decision
		}
		if decision.err != nil {
			return nil, decision.err
		}
		if !decision.allowed {
			results[i].Claims = nil
			results[i].Err = workercontrolcore.ErrDenied
		}
	}
	return results, ctx.Err()
}

func verifyViewerLoginProbes(ctx context.Context, pool *pgxpool.Pool, probes map[viewerCredentialProbe]bool) error {
	if len(probes) == 0 {
		return nil
	}
	ordered := make([]viewerCredentialProbe, 0, len(probes))
	ids, profiles := make([]string, 0, len(probes)), make([]string, 0, len(probes))
	users := make([]int32, 0, len(probes))
	remote := make([]bool, 0, len(probes))
	for probe := range probes {
		if probe.user <= 0 || probe.user > math.MaxInt32 {
			return workercontrolcore.ErrOutputRevoked
		}
		ordered = append(ordered, probe)
		ids = append(ids, probe.login)
		profiles = append(profiles, probe.profile)
		users = append(users, int32(probe.user))
		remote = append(remote, probe.remotePlayback)
	}
	rows, err := pool.Query(ctx, `SELECT p.ordinality, EXISTS (
SELECT 1 FROM auth_sessions s JOIN users u ON u.id=s.user_id
WHERE s.id=p.id AND s.user_id=p.user_id AND s.revoked_at IS NULL AND s.expires_at>NOW()
AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at>NOW()) AND u.enabled
AND (s.purpose!='remote_playback' OR s.bound_profile_id=p.profile_id AND s.bound_profile_id!='' AND p.remote_allowed)
) FROM unnest($1::text[],$2::integer[],$3::text[],$4::boolean[]) WITH ORDINALITY p(id,user_id,profile_id,remote_allowed,ordinality)`, ids, users, profiles, remote)
	if err != nil {
		return err
	}
	return collectViewerProbeResults(rows, ordered, probes)
}

func verifyViewerKeyProbes(ctx context.Context, pool *pgxpool.Pool, probes map[viewerCredentialProbe]bool) error {
	if len(probes) == 0 {
		return nil
	}
	ordered := make([]viewerCredentialProbe, 0, len(probes))
	ids := make([]int64, 0, len(probes))
	users := make([]int32, 0, len(probes))
	for probe := range probes {
		if probe.user <= 0 || probe.user > math.MaxInt32 {
			return workercontrolcore.ErrOutputRevoked
		}
		ordered = append(ordered, probe)
		ids = append(ids, probe.key)
		users = append(users, int32(probe.user))
	}
	rows, err := pool.Query(ctx, `SELECT p.ordinality, EXISTS (
SELECT 1 FROM api_keys k JOIN users u ON u.id=k.user_id
WHERE k.id=p.id AND k.user_id=p.user_id AND cardinality(k.scopes)=0 AND u.enabled
) FROM unnest($1::bigint[],$2::integer[]) WITH ORDINALITY p(id,user_id,ordinality)`, ids, users)
	if err != nil {
		return err
	}
	return collectViewerProbeResults(rows, ordered, probes)
}

func collectViewerProbeResults(rows pgx.Rows, ordered []viewerCredentialProbe, probes map[viewerCredentialProbe]bool) error {
	defer rows.Close()
	seen := make([]bool, len(ordered))
	count := 0
	for rows.Next() {
		var ordinal int64
		var allowed bool
		if err := rows.Scan(&ordinal, &allowed); err != nil {
			return err
		}
		if ordinal <= 0 || ordinal > int64(len(ordered)) || seen[ordinal-1] {
			return workercontrolcore.ErrUnavailable
		}
		seen[ordinal-1] = true
		count++
		probes[ordered[ordinal-1]] = allowed
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(ordered) {
		return workercontrolcore.ErrUnavailable
	}
	return nil
}
