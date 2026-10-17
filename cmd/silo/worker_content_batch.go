package main

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
)

const workerContentBatchSize = 512

type workerSnapshotContextKey struct{}
type workerContentRequest struct {
	states map[*workerAccessState]*workerContentFacts
}
type workerScopeKey struct {
	user    int
	profile string
}
type workerUserFact struct {
	user   *models.User
	policy access.EffectiveUserPolicy
}
type workerScopeFact struct {
	scope   access.Scope
	allowed bool
}
type workerThemeKey struct {
	scope workerScopeKey
	path  string
}

// Facts belong to one snapshot only. Execution collection runs concurrently,
// but content batches consume these maps sequentially after that collection.
type workerContentFacts struct {
	state     *workerAccessState
	users     map[int]workerUserFact
	scopes    map[workerScopeKey]workerScopeFact
	files     map[int]*models.MediaFile
	folders   map[int]bool
	episodes  map[string]*models.Episode
	extras    map[string]*models.MediaExtra
	artifacts map[string]*downloads.Artifact
	access    map[workerScopeKey]map[string]bool
	themes    map[workerThemeKey]bool
}

func newWorkerSnapshotContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, workerSnapshotContextKey{}, &workerContentRequest{states: map[*workerAccessState]*workerContentFacts{}})
}

func workerFacts(ctx context.Context, state *workerAccessState) *workerContentFacts {
	request, ok := ctx.Value(workerSnapshotContextKey{}).(*workerContentRequest)
	if ok {
		if facts := request.states[state]; facts != nil {
			return facts
		}
	}
	facts := &workerContentFacts{state: state, users: map[int]workerUserFact{}, scopes: map[workerScopeKey]workerScopeFact{}, files: map[int]*models.MediaFile{}, folders: map[int]bool{}, episodes: map[string]*models.Episode{}, extras: map[string]*models.MediaExtra{}, artifacts: map[string]*downloads.Artifact{}, access: map[workerScopeKey]map[string]bool{}, themes: map[workerThemeKey]bool{}}
	if ok {
		request.states[state] = facts
	}
	return facts
}

func (f *workerContentFacts) user(ctx context.Context, id int) (workerUserFact, error) {
	if fact, found := f.users[id]; found {
		return fact, nil
	}
	var fact workerUserFact
	user, err := f.state.users.GetByID(ctx, id)
	if err != nil {
		_, err = workerContentReadError(err)
		if err == nil {
			f.users[id] = fact
		}
		return fact, err
	}
	if user != nil && user.Enabled {
		policy, err := access.EffectivePolicyForUser(ctx, user, f.state.groups)
		if err != nil {
			_, err = workerContentReadError(err)
			if err == nil {
				f.users[id] = fact
			}
			return fact, err
		}
		fact.user, fact.policy = user, policy
	}
	f.users[id] = fact
	return fact, nil
}

func (f *workerContentFacts) scope(ctx context.Context, key workerScopeKey) (workerScopeFact, error) {
	if fact, found := f.scopes[key]; found {
		return fact, nil
	}
	var fact workerScopeFact
	scope, err := f.state.resolver.Resolve(ctx, access.ResolveInput{UserID: key.user, ProfileID: key.profile, SkipPINVerification: true})
	if err != nil {
		_, err = workerContentReadError(err)
		if err == nil {
			f.scopes[key] = fact
		}
		return fact, err
	}
	if scope.PreferencesDegraded {
		return fact, workercontrolcore.ErrUnavailable
	}
	fact.scope, fact.allowed = scope, scope.ProfileVerified
	f.scopes[key] = fact
	return fact, nil
}

type workerContentCandidate struct {
	index    int
	card     *playback.RecipeCard
	prepared *streamtoken.Claims
	scope    workerScopeKey
	filter   catalog.AccessFilter
	owner    string
}

// checkRecipes applies account and profile checks before loading shared
// catalog facts in bounded batches. Query count depends on distinct scopes
// and batches, rather than on each work or each viewer of a media file.
func (a *workerContentAuthority) checkRecipes(ctx context.Context, cards []*playback.RecipeCard, prepared []*streamtoken.Claims) ([]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(cards) != len(prepared) {
		return nil, workercontrolcore.ErrUnavailable
	}
	out := make([]bool, len(cards))
	valid := false
	for _, card := range cards {
		valid = valid || card != nil && card.UserID > 0
	}
	if !valid {
		return out, nil
	}
	if a == nil {
		return nil, workercontrolcore.ErrUnavailable
	}
	state := a.state.Load()
	if state == nil || state.resolver == nil {
		return nil, workercontrolcore.ErrUnavailable
	}
	facts := workerFacts(ctx, state)
	var candidates []workerContentCandidate
	for i, card := range cards {
		candidate, eligible, err := facts.candidate(ctx, i, card, prepared[i])
		if err != nil {
			return nil, err
		}
		if eligible {
			candidates = append(candidates, candidate)
		}
	}
	if err := facts.loadMedia(ctx, candidates); err != nil {
		return nil, err
	}
	var media []workerContentCandidate
	for _, candidate := range candidates {
		card := candidate.card
		if card.MediaFileID == 0 {
			allowed, err := facts.theme(ctx, candidate)
			if err != nil {
				return nil, err
			}
			out[candidate.index] = allowed
			continue
		}
		candidate.owner = facts.mediaOwner(candidate)
		if candidate.owner != "" {
			media = append(media, candidate)
		}
	}
	if err := facts.loadItemAccess(ctx, media); err != nil {
		return nil, err
	}
	for _, candidate := range media {
		out[candidate.index] = facts.access[candidate.scope][candidate.owner]
	}
	return out, ctx.Err()
}

func (f *workerContentFacts) candidate(ctx context.Context, index int, card *playback.RecipeCard, prepared *streamtoken.Claims) (workerContentCandidate, bool, error) {
	if card == nil || card.UserID <= 0 {
		return workerContentCandidate{}, false, nil
	}
	user, err := f.user(ctx, card.UserID)
	if err != nil {
		return workerContentCandidate{}, false, err
	}
	if user.user == nil {
		return workerContentCandidate{}, false, nil
	}
	if prepared != nil {
		if !user.policy.DownloadAllowed {
			return workerContentCandidate{}, false, nil
		}
	} else {
		method := string(card.PlayMethod)
		videoEncode := (method == "" || strings.HasPrefix(method, "transcode")) && !card.AudioOnly && card.TargetCodecVideo != "copy"
		if videoEncode && !user.policy.TranscodeAllowed || (card.TranscodeAudio || method == streamtoken.PlayMethodThemeAAC) && !user.policy.AudioTranscodeAllowed {
			return workerContentCandidate{}, false, nil
		}
	}
	key := workerScopeKey{user: card.UserID, profile: card.ProfileID}
	scope, err := f.scope(ctx, key)
	if err != nil {
		return workerContentCandidate{}, false, err
	}
	if !scope.allowed {
		return workerContentCandidate{}, false, nil
	}
	filter := catalog.AccessFilter{AllowedLibraryIDs: scope.scope.AllowedLibraryIDs, DisabledLibraryIDs: scope.scope.DisabledLibraryIDs, MaturityLimits: scope.scope.MaturityLimits, MaxPlaybackQuality: scope.scope.MaxPlaybackQuality, UserID: card.UserID, ProfileID: card.ProfileID}
	return workerContentCandidate{index: index, card: card, prepared: prepared, scope: key, filter: filter}, true, nil
}

func (f *workerContentFacts) mediaOwner(candidate workerContentCandidate) string {
	card := candidate.card
	file := f.files[card.MediaFileID]
	if file == nil || candidate.prepared == nil && file.MissingSince != nil {
		return ""
	}
	// Ordinary recipes retain the admitted logical file path. A different
	// catalog row at that old path cannot authorize a retargeted MediaFileID.
	// Prepared output instead binds the exact ready artifact below and may
	// outlive its original source file.
	if candidate.prepared == nil && file.FilePath != card.InputPath {
		return ""
	}
	if candidate.prepared != nil {
		artifact := f.artifacts[candidate.prepared.DownloadArtifactRowID]
		if !workerPreparedArtifactMatches(artifact, candidate.prepared) {
			return ""
		}
		served := *file
		if artifact.Resolution != "" {
			served.Resolution = artifact.Resolution
		}
		file = &served
	}
	if !catalog.FileAllowedByAccess(file, candidate.filter) || !f.folders[file.MediaFolderID] {
		return ""
	}
	owner := file.ContentID
	if file.EpisodeID != "" {
		episode := f.episodes[file.EpisodeID]
		if episode == nil {
			return ""
		}
		owner = episode.SeriesID
	}
	if file.ExtraID != "" {
		extra := f.extras[file.ExtraID]
		if extra == nil {
			return ""
		}
		owner = extra.ParentID
	}
	return owner
}

func uncachedWorkerIDs[K comparable, V any](cache map[K]V, ids []K) []K {
	seen := map[K]bool{}
	out := make([]K, 0, len(ids))
	for _, id := range ids {
		if _, found := cache[id]; !found && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (f *workerContentFacts) loadMedia(ctx context.Context, candidates []workerContentCandidate) error {
	var files []int
	var artifacts []string
	for _, candidate := range candidates {
		if candidate.card.MediaFileID == 0 {
			continue
		}
		files = append(files, candidate.card.MediaFileID)
		if candidate.prepared != nil && candidate.prepared.DownloadArtifactRowID != "" {
			artifacts = append(artifacts, candidate.prepared.DownloadArtifactRowID)
		}
	}
	var folders []int
	var episodes, extras []string
	for ids := range slices.Chunk(uncachedWorkerIDs(f.files, files), workerContentBatchSize) {
		rows, err := f.state.files.GetByIDs(ctx, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			f.files[id] = nil
		}
		for _, row := range rows {
			f.files[row.ID] = row
			folders = append(folders, row.MediaFolderID)
			if row.EpisodeID != "" {
				episodes = append(episodes, row.EpisodeID)
			}
			if row.ExtraID != "" {
				extras = append(extras, row.ExtraID)
			}
		}
	}
	for ids := range slices.Chunk(uncachedWorkerIDs(f.folders, folders), workerContentBatchSize) {
		if err := f.loadFolders(ctx, ids); err != nil {
			return err
		}
	}
	for ids := range slices.Chunk(uncachedWorkerIDs(f.episodes, episodes), workerContentBatchSize) {
		rows, err := f.state.episodes.GetByIDs(ctx, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			f.episodes[id] = nil
		}
		for _, row := range rows {
			f.episodes[row.ContentID] = row
		}
	}
	for ids := range slices.Chunk(uncachedWorkerIDs(f.extras, extras), workerContentBatchSize) {
		rows, err := f.state.extras.GetByIDs(ctx, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			f.extras[id] = nil
		}
		for _, row := range rows {
			f.extras[row.ContentID] = row
		}
	}
	for ids := range slices.Chunk(uncachedWorkerIDs(f.artifacts, artifacts), workerContentBatchSize) {
		rows, err := f.state.artifacts.GetByIDs(ctx, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			f.artifacts[id] = nil
		}
		for _, row := range rows {
			f.artifacts[row.ID] = row
		}
	}
	return ctx.Err()
}

func (f *workerContentFacts) loadFolders(ctx context.Context, ids []int) error {
	rows, err := f.state.pool.Query(ctx, "SELECT id,enabled FROM media_folders WHERE id=ANY($1::bigint[])", ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for _, id := range ids {
		f.folders[id] = false
	}
	for rows.Next() {
		var id int
		var enabled bool
		if err := rows.Scan(&id, &enabled); err != nil {
			return err
		}
		f.folders[id] = enabled
	}
	return rows.Err()
}

func (f *workerContentFacts) loadItemAccess(ctx context.Context, candidates []workerContentCandidate) error {
	groups := map[workerScopeKey][]string{}
	filters := map[workerScopeKey]catalog.AccessFilter{}
	for _, candidate := range candidates {
		groups[candidate.scope] = append(groups[candidate.scope], candidate.owner)
		filters[candidate.scope] = candidate.filter
	}
	for scope, owners := range groups {
		if f.access[scope] == nil {
			f.access[scope] = map[string]bool{}
		}
		for ids := range slices.Chunk(uncachedWorkerIDs(f.access[scope], owners), workerContentBatchSize) {
			allowed, err := f.state.items.EnsureAccessibleIDs(ctx, ids, filters[scope])
			if err != nil {
				return err
			}
			for _, id := range ids {
				f.access[scope][id] = allowed[id]
			}
		}
	}
	return ctx.Err()
}

func (f *workerContentFacts) theme(ctx context.Context, candidate workerContentCandidate) (bool, error) {
	key := workerThemeKey{scope: candidate.scope, path: candidate.card.InputPath}
	if allowed, found := f.themes[key]; found {
		return allowed, nil
	}
	var id int64
	if err := f.state.pool.QueryRow(ctx, "SELECT id FROM item_theme_songs WHERE file_path=$1", key.path).Scan(&id); err != nil {
		allowed, readErr := workerContentReadError(err)
		if readErr == nil {
			f.themes[key] = allowed
		}
		return allowed, readErr
	}
	file, err := f.state.themes.Find(ctx, strconv.FormatInt(id, 10), candidate.filter)
	if err != nil {
		allowed, readErr := workerContentReadError(err)
		if readErr == nil {
			f.themes[key] = allowed
		}
		return allowed, readErr
	}
	allowed := file.Path == key.path
	f.themes[key] = allowed
	return allowed, nil
}
