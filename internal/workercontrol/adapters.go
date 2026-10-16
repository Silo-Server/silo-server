package workercontrol

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type localConfig struct {
	mu            sync.RWMutex
	cfg           *config.Config
	nodeID        int
	nodeURL       string
	maxJobs       int
	execution     workerwire.ExecutionConfig
	drainCallback func(bool)
	refresh       func(context.Context) error
}

const (
	transcodeMode   = "transcode"
	generationField = "generation"
	inputPathField  = "input_path"
)

func newLocalConfig(options Options) *localConfig {
	cfg := &config.Config{}
	cfg.Server.Mode = transcodeMode
	cfg.Playback.FFmpegPath = options.FFmpeg
	cfg.Playback.HWAccel = options.HWAccel
	cfg.Playback.HWDevice = options.HWDevice
	cfg.Playback.TranscodeDir = options.Scratch
	cfg.Playback.SegmentRetentionSeconds = 600
	cfg.Playback.SubtitleSyncNodeCapacity = 1
	return &localConfig{cfg: cfg, nodeID: options.Binding.NodeID, nodeURL: options.Binding.NodeURL, maxJobs: options.MaxJobs}
}
func (c *localConfig) NodeRowID() (int, bool)            { return c.nodeID, c.nodeID > 0 }
func (c *localConfig) NodeRegisteredURL() (string, bool) { return c.nodeURL, c.nodeID > 0 }

type inputAuthorizer struct{ adapter *workerinput.Adapter }

func (a inputAuthorizer) Allowed(_ context.Context, path string) (bool, error) {
	return a.adapter.Allowed(path), nil
}
func (a inputAuthorizer) AllowedTheme(_ context.Context, _ int64, path string, _ int64, _ time.Time) (bool, error) {
	return a.adapter.Allowed(path), nil
}

type reportTracker struct {
	runtime       *Runtime
	mu            sync.Mutex
	sessions      map[string]nodesessions.SessionInfo
	scopes        map[string]workerwire.SessionReport
	registrations map[string]*reportRegistration
}

func (t *reportTracker) Track(ctx context.Context, info nodesessions.SessionInfo) {
	if registration := t.PrepareSessionTracking(ctx, info); registration != nil {
		registration.Track(ctx)
	}
}

// Remove cannot infer an instance from a session ID. The prepared worker
// engine retires the exact receipt; the legacy callback has no deletion rights.
func (t *reportTracker) Remove(context.Context, string) {}
func (t *reportTracker) Cleanup(context.Context) {
	t.mu.Lock()
	clear(t.sessions)
	clear(t.scopes)
	clear(t.registrations)
	t.mu.Unlock()
}
func (t *reportTracker) NodeURL() string  { return t.runtime.options.Binding.NodeURL }
func (t *reportTracker) NodeName() string { return "HTTP worker" }
func (t *reportTracker) snapshot() []nodesessions.SessionInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]nodesessions.SessionInfo, 0, len(t.sessions))
	for _, info := range t.sessions {
		result = append(result, info)
	}
	return result
}
func (t *reportTracker) scopedSnapshot() []workerwire.SessionReport {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]workerwire.SessionReport, 0, len(t.scopes))
	for _, scope := range t.scopes {
		if scope.Generation > 0 {
			result = append(result, scope)
		}
	}
	return result
}
func (r *Runtime) reportLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var sequence uint64
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
		sequence++
		ctx, cancel := context.WithTimeout(r.ctx, time.Second)
		var ack struct {
			Accepted bool `json:"accepted"`
		}
		err := r.callGrant(ctx, "POST", "/report", workerwire.Report{BootID: r.bootID, Sequence: sequence, Sessions: r.tracker.scopedSnapshot(), Observations: r.observations.batch()}, "silo/worker-report/v1", &ack)
		if err == nil && ack.Accepted {
			r.observations.ack()
		}
		cancel()
	}
}

type recipeResponse struct {
	Assignment workerwire.Assignment `json:"assignment"`
	Recipe     playback.RecipeCard   `json:"recipe"`
}

func (r *Runtime) currentRecipe(ctx context.Context, workID string) (*playback.RecipeCard, error) {
	var response recipeResponse
	var grant workeridentity.Grant
	if err := r.callGrant(ctx, "GET", "/recipes/"+url.PathEscape(workID), nil, "silo/worker-recipe/v1", &response, &grant); err != nil {
		return nil, err
	}
	if response.Assignment.NodeID != r.options.Binding.NodeID || response.Assignment.WorkID != workID || response.Assignment.Version != 1 || grant.WorkID != workID || grant.Generation != response.Assignment.Generation {
		return nil, errors.New("recipe scope mismatch")
	}
	if err := r.refresh(ctx); err != nil || !r.workAllowed(workID, response.Assignment.Generation) {
		return nil, errors.New("recipe authority unavailable")
	}
	handles, err := r.openSources(response.Assignment)
	if err != nil {
		return nil, err
	}
	h := handles[response.Recipe.InputPath]
	if h == nil {
		return nil, errors.New("recipe source is not admitted")
	}
	response.Recipe.InputPath = h.URL()
	return &response.Recipe, nil
}

type recipeAdapter struct{ runtime *Runtime }

func (a recipeAdapter) Get(ctx context.Context, id string) (*playback.RecipeCard, bool) {
	if a.runtime.coreStopped(ctx, id) {
		return nil, false
	}
	card, err := a.runtime.currentRecipe(ctx, id)
	return card, err == nil
}
func (a recipeAdapter) Delete(ctx context.Context, id string) error {
	if a.runtime.coreStopped(ctx, id) {
		return nil
	}
	var ack struct {
		Accepted bool `json:"accepted"`
	}
	generation, _ := ctx.Value(generationContextKey{}).(uint64)
	if generation == 0 {
		a.runtime.mu.Lock()
		generation = a.runtime.generations[id]
		a.runtime.mu.Unlock()
	}
	if generation == 0 {
		return errors.New("close requires assignment generation")
	}
	return a.runtime.callGrant(ctx, "DELETE", "/recipes/"+url.PathEscape(id), map[string]uint64{generationField: generation}, "silo/worker-close/v1", &ack)
}
func (a recipeAdapter) RevokeNode(ctx context.Context, _ string) error {
	var ack struct {
		Accepted bool `json:"accepted"`
	}
	return a.runtime.callGrant(ctx, "POST", "/revoke", nil, "silo/worker-close/v1", &ack)
}
func (a recipeAdapter) RevokeNodeID(ctx context.Context, _ int) error { return a.RevokeNode(ctx, "") }

//nolint:nestif // Playback recipes and prepared artifacts retain distinct source and live-generation checks before caching a verified token.
func (r *Runtime) verifyToken(ctx context.Context, token string) (*streamtoken.Claims, error) {
	if strings.Count(token, ".") != 1 {
		return nil, errors.New("worker output requires a scoped core grant")
	}
	r.mu.Lock()
	entry, cached := r.tokens[token]
	r.mu.Unlock()
	if cached && entry.epoch == r.config.OutputAuthorizationEpoch() && time.Now().Before(entry.expires) && r.workAllowed(entry.workID, entry.generation) && r.authority.OutputAllowed(entry.authorityID) {
		snapshot := *entry.claims
		return &snapshot, nil
	}
	grant, authority, inner, err := r.decodeOutput(token)
	if err != nil {
		return nil, err
	}
	claims := *inner
	authorityID := authority.AuthorityID
	workID := claims.TranscodeTransportID
	if workID == "" {
		workID = claims.SessionID
	}
	if claims.DownloadArtifactID != "" {
		workID = claims.DownloadArtifactID
	}
	if workID == "" || grant.WorkID != workID || grant.Generation == 0 {
		return nil, errors.New("output grant has no matching work identity")
	}
	if authorityID == "" || !r.authority.OutputAllowed(authorityID) {
		return nil, errors.New("viewer authority unavailable")
	}
	if claims.DownloadArtifactID == "" {
		card, err := r.currentRecipe(ctx, workID)
		if err != nil {
			return nil, err
		}
		claims.MediaPath = card.InputPath
	} else {
		if !r.workAllowed(workID, grant.Generation) {
			if err := r.refresh(ctx); err != nil {
				return nil, err
			}
		}
		if !r.workAllowed(workID, grant.Generation) {
			return nil, errors.New("artifact authority unavailable")
		}
		r.mu.Lock()
		r.generations[workID] = grant.Generation
		r.tracker.retireGenerationLocked(workID, grant.Generation, true)
		r.mu.Unlock()
		claims.MediaPath = ""
	}
	r.mu.Lock()
	if r.generations[workID] != grant.Generation {
		r.mu.Unlock()
		return nil, errors.New("output grant generation changed")
	}
	if len(r.tokens) >= 4096 {
		clear(r.tokens)
	}
	expires := time.Unix(grant.ExpiresAt, 0)
	if claims.ExpiresAt != nil && claims.ExpiresAt.Before(expires) {
		expires = claims.ExpiresAt.Time
	}
	r.tokens[token] = tokenEntry{claims: &claims, workID: workID, generation: r.generations[workID], expires: expires, epoch: r.config.OutputAuthorizationEpoch(), authorityID: authorityID}
	r.mu.Unlock()
	return &claims, nil
}
