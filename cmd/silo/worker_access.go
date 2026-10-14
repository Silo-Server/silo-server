package main

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type workerScopeResolver interface {
	Resolve(context.Context, access.ResolveInput) (access.Scope, error)
}
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
	prepared := preparedClaims != nil
	s := a.state.Load()
	if s == nil || card == nil || card.UserID <= 0 {
		return false
	}
	user, err := s.users.GetByID(ctx, card.UserID)
	if err != nil || user == nil || !user.Enabled {
		return false
	}
	policy, err := access.EffectivePolicyForUser(ctx, user, s.groups)
	if err != nil || prepared && !policy.DownloadAllowed {
		return false
	}
	if !prepared {
		method := string(card.PlayMethod)
		videoEncode := (method == "" || strings.HasPrefix(method, "transcode")) && !card.AudioOnly && card.TargetCodecVideo != "copy"
		if videoEncode && !policy.TranscodeAllowed || (card.TranscodeAudio || method == streamtoken.PlayMethodThemeAAC) && !policy.AudioTranscodeAllowed {
			return false
		}
	}
	scope, err := s.resolver.Resolve(ctx, access.ResolveInput{UserID: card.UserID, ProfileID: card.ProfileID, SkipPINVerification: true})
	if err != nil || !scope.ProfileVerified || scope.PreferencesDegraded {
		return false
	}
	filter := catalog.AccessFilter{AllowedLibraryIDs: scope.AllowedLibraryIDs, DisabledLibraryIDs: scope.DisabledLibraryIDs, MaturityLimits: scope.MaturityLimits, MaxPlaybackQuality: scope.MaxPlaybackQuality, UserID: card.UserID, ProfileID: card.ProfileID}
	if card.MediaFileID == 0 {
		var id int64
		if err := s.pool.QueryRow(ctx, "SELECT id FROM item_theme_songs WHERE file_path=$1", card.InputPath).Scan(&id); err != nil {
			return false
		}
		file, err := s.themes.Find(ctx, strconv.FormatInt(id, 10), filter)
		return err == nil && file.Path == card.InputPath
	}
	file, err := s.files.GetByID(ctx, card.MediaFileID)
	if err != nil || file == nil || !prepared && file.MissingSince != nil {
		return false
	}
	if prepared {
		artifact, err := s.artifacts.GetByID(ctx, preparedClaims.DownloadArtifactRowID)
		if err != nil || !workerPreparedArtifactMatches(artifact, preparedClaims) {
			return false
		}
		// Apply the quality ceiling to the bytes served, while retaining the
		// source's library and catalog ownership restrictions.
		served := *file
		if artifact.Resolution != "" {
			served.Resolution = artifact.Resolution
		}
		file = &served
	}
	if !catalog.FileAllowedByAccess(file, filter) {
		return false
	}
	var enabled bool
	if err := s.pool.QueryRow(ctx, "SELECT enabled FROM media_folders WHERE id=$1", file.MediaFolderID).Scan(&enabled); err != nil || !enabled {
		return false
	}
	owner := file.ContentID
	if file.EpisodeID != "" {
		episode, err := s.episodes.GetByID(ctx, file.EpisodeID)
		if err != nil || episode == nil {
			return false
		}
		owner = episode.SeriesID
	}
	if file.ExtraID != "" {
		extra, err := s.extras.GetByID(ctx, file.ExtraID)
		if err != nil || extra == nil {
			return false
		}
		owner = extra.ParentID
	}
	return owner != "" && s.items.EnsureAccessible(ctx, owner, filter) == nil
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
