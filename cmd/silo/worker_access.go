package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type workerScopeResolver = scopeResolver
type workerAccessState struct {
	pool      *pgxpool.Pool
	resolver  workerScopeResolver
	users     *auth.UserRepository
	files     *scanner.FileRepository
	items     *catalog.ItemRepository
	episodes  *catalog.EpisodeRepository
	extras    *catalog.ExtraRepository
	themes    *themesongs.Repository
	groups    *access.GroupStore
	artifacts *downloads.ArtifactRepository
}
type workerContentAuthority struct {
	state atomic.Pointer[workerAccessState]
}

func (a *workerContentAuthority) configure(pool *pgxpool.Pool, resolver workerScopeResolver) {
	a.state.Store(&workerAccessState{pool: pool, resolver: resolver, users: auth.NewUserRepository(pool), files: scanner.NewFileRepository(pool), items: catalog.NewItemRepository(pool), episodes: catalog.NewEpisodeRepository(pool), extras: catalog.NewExtraRepository(pool), themes: themesongs.NewRepository(pool), groups: access.NewGroupStore(pool), artifacts: downloads.NewArtifactRepository(pool)})
}

// A durable recipe proves the original admission, not continuing account,
// profile or catalog permissions. Re-resolve them on the core authority path.
func (a *workerContentAuthority) allowed(ctx context.Context, card *playback.RecipeCard) bool {
	return a.allowedRecipe(ctx, card, nil)
}

func (a *workerContentAuthority) allowedClaims(ctx context.Context, claims *streamtoken.Claims) bool {
	if claims == nil {
		return false
	}
	card := playback.RecipeCardFromClaims(claims)
	if claims.DownloadArtifactID != "" {
		return a.allowedRecipe(ctx, &card, claims)
	}
	return a.allowedRecipe(ctx, &card, nil)
}

func (a *workerContentAuthority) allowedRecipe(ctx context.Context, card *playback.RecipeCard, preparedClaims *streamtoken.Claims) bool {
	allowed, _ := a.checkRecipe(ctx, card, preparedClaims)
	return allowed
}

func (a *workerContentAuthority) checkClaims(ctx context.Context, claims *streamtoken.Claims) (bool, error) {
	if claims == nil {
		return false, nil
	}
	card := playback.RecipeCardFromClaims(claims)
	if claims.DownloadArtifactID != "" {
		return a.checkRecipe(ctx, &card, claims)
	}
	return a.checkRecipe(ctx, &card, nil)
}

//nolint:funlen,gocyclo // Keep the ordered account, scope, catalog and media checks together so every definitive denial remains visible.
func (a *workerContentAuthority) checkRecipe(ctx context.Context, card *playback.RecipeCard, preparedClaims *streamtoken.Claims) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if card == nil || card.UserID <= 0 {
		return false, nil
	}
	if a == nil {
		return false, workercontrolcore.ErrUnavailable
	}
	s := a.state.Load()
	if s == nil || s.resolver == nil {
		return false, workercontrolcore.ErrUnavailable
	}
	prepared := preparedClaims != nil
	user, err := s.users.GetByID(ctx, card.UserID)
	if err != nil {
		return workerContentReadError(err)
	}
	if user == nil || !user.Enabled {
		return false, nil
	}
	policy, err := access.EffectivePolicyForUser(ctx, user, s.groups)
	if err != nil {
		return workerContentReadError(err)
	}
	if prepared && !policy.DownloadAllowed {
		return false, nil
	}
	if !prepared {
		method := string(card.PlayMethod)
		videoEncode := (method == "" || strings.HasPrefix(method, "transcode")) && !card.AudioOnly && card.TargetCodecVideo != "copy"
		if videoEncode && !policy.TranscodeAllowed || (card.TranscodeAudio || method == streamtoken.PlayMethodThemeAAC) && !policy.AudioTranscodeAllowed {
			return false, nil
		}
	}
	scope, err := s.resolver.Resolve(ctx, access.ResolveInput{UserID: card.UserID, ProfileID: card.ProfileID, SkipPINVerification: true})
	if err != nil {
		return workerContentReadError(err)
	}
	if scope.PreferencesDegraded {
		return false, workercontrolcore.ErrUnavailable
	}
	if !scope.ProfileVerified {
		return false, nil
	}
	filter := catalog.AccessFilter{AllowedLibraryIDs: scope.AllowedLibraryIDs, DisabledLibraryIDs: scope.DisabledLibraryIDs, MaturityLimits: scope.MaturityLimits, MaxPlaybackQuality: scope.MaxPlaybackQuality, UserID: card.UserID, ProfileID: card.ProfileID}
	if card.MediaFileID == 0 {
		var id int64
		if err := s.pool.QueryRow(ctx, "SELECT id FROM item_theme_songs WHERE file_path=$1", card.InputPath).Scan(&id); err != nil {
			return workerContentReadError(err)
		}
		file, err := s.themes.Find(ctx, strconv.FormatInt(id, 10), filter)
		if err != nil {
			return workerContentReadError(err)
		}
		return file.Path == card.InputPath, nil
	}
	file, err := s.files.GetByID(ctx, card.MediaFileID)
	if err != nil {
		return workerContentReadError(err)
	}
	if file == nil || !prepared && file.MissingSince != nil {
		return false, nil
	}
	if prepared {
		artifact, err := s.artifacts.GetByID(ctx, preparedClaims.DownloadArtifactRowID)
		if err != nil {
			return workerContentReadError(err)
		}
		if !workerPreparedArtifactMatches(artifact, preparedClaims) {
			return false, nil
		}
		served := *file
		if artifact.Resolution != "" {
			served.Resolution = artifact.Resolution
		}
		file = &served
	}
	if !catalog.FileAllowedByAccess(file, filter) {
		return false, nil
	}
	var enabled bool
	if err := s.pool.QueryRow(ctx, "SELECT enabled FROM media_folders WHERE id=$1", file.MediaFolderID).Scan(&enabled); err != nil {
		return workerContentReadError(err)
	}
	if !enabled {
		return false, nil
	}
	owner := file.ContentID
	if file.EpisodeID != "" {
		episode, err := s.episodes.GetByID(ctx, file.EpisodeID)
		if err != nil {
			return workerContentReadError(err)
		}
		if episode == nil {
			return false, nil
		}
		owner = episode.SeriesID
	}
	if file.ExtraID != "" {
		extra, err := s.extras.GetByID(ctx, file.ExtraID)
		if err != nil {
			return workerContentReadError(err)
		}
		if extra == nil {
			return false, nil
		}
		owner = extra.ParentID
	}
	if owner == "" {
		return false, nil
	}
	if err := s.items.EnsureAccessible(ctx, owner, filter); err != nil {
		return workerContentReadError(err)
	}
	return true, ctx.Err()
}

// Missing or out-of-scope rows are authoritative denials. Dependency errors
// are unknown and must not mint a new signed worker authority lease.
func workerContentReadError(err error) (bool, error) {
	for _, missing := range []error{pgx.ErrNoRows, auth.ErrNotFound, scanner.ErrFileNotFound, catalog.ErrItemNotFound, catalog.ErrEpisodeNotFound, catalog.ErrExtraNotFound, downloads.ErrNotFound, themesongs.ErrNotFound, access.ErrProfileNotFound, access.ErrProfileUnverified, access.ErrGroupNotFound} {
		if errors.Is(err, missing) {
			return false, nil
		}
	}
	return false, err
}

func workerPreparedArtifactMatches(artifact *downloads.Artifact, claims *streamtoken.Claims) bool {
	if artifact == nil || artifact.MediaFileID != claims.MediaFileID ||
		artifact.OriginArtifactID != claims.DownloadArtifactID || !workeridentity.SameOrigin(artifact.OriginNodeURL, claims.TranscodeNode, strings.HasPrefix(strings.ToLower(artifact.OriginNodeURL), "http://")) ||
		claims.RoutingExecutionNodeID != 0 && artifact.OriginNodeID != claims.RoutingExecutionNodeID ||
		claims.DownloadExecutionFingerprint != "" && artifact.ParamsHash != claims.DownloadExecutionFingerprint {
		return false
	}
	switch artifact.Status {
	case downloads.ArtifactReady, downloads.ArtifactToneMapReady, downloads.ArtifactAudioV2Ready, downloads.ArtifactTracksReady:
		return true
	default:
		return false
	}
}
