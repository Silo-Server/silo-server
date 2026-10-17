package main

import (
	"context"
	"errors"
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

func (a *workerContentAuthority) checkRecipe(ctx context.Context, card *playback.RecipeCard, preparedClaims *streamtoken.Claims) (bool, error) {
	allowed, err := a.checkRecipes(ctx, []*playback.RecipeCard{card}, []*streamtoken.Claims{preparedClaims})
	if err != nil {
		return false, err
	}
	return allowed[0], nil
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
