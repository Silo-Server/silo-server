package transcodenode

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// testSecret is a shared secret the node does not hold: a request signed
// with it must not pass the node's own key check.
const testSecret = "shared-secret-the-node-does-not-hold"

// testRuntime is the node's configuration as tests set it, in place of the
// core's answers.
type testRuntime struct {
	mu        sync.Mutex
	cfg       *config.Config
	nodeID    int
	drain     bool
	onDrain   []func(bool)
	reloadErr error
	reloads   int
}

func newTestRuntime(cfg *config.Config) *testRuntime { return &testRuntime{cfg: cfg} }

func (r *testRuntime) Config() *config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

func (r *testRuntime) NodeRowID() (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nodeID, r.nodeID > 0
}

func (r *testRuntime) NodeDrainRequested() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drain
}

func (r *testRuntime) OnNodeDrainChange(fn func(bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onDrain = append(r.onDrain, fn)
}

func (r *testRuntime) ForceReload(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloads++
	return r.reloadErr
}

// SetConfigForTest replaces the configuration, as a changed document would.
func (r *testRuntime) SetConfigForTest(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
}

// SetNodeDrainRequestedForTest sets the drain request and runs the drain
// callbacks when it changes, as a read of a changed document would.
func (r *testRuntime) SetNodeDrainRequestedForTest(requested bool) {
	r.mu.Lock()
	changed := r.drain != requested
	r.drain = requested
	callbacks := slices.Clone(r.onDrain)
	r.mu.Unlock()
	if changed {
		for _, fn := range callbacks {
			fn(requested)
		}
	}
}

// runtimeOf is the test configuration a test server runs with.
func runtimeOf(s *Server) *testRuntime { return s.watcher.(*testRuntime) }

// pathSources opens a grant that is the source's path: tests send the path
// they want FFmpeg to read as the job's source_grant. refused answers a
// grant with an error instead.
type pathSources struct {
	mu      sync.Mutex
	refused map[string]error
	opened  []*testSource
}

func (p *pathSources) Open(_ context.Context, grant string) (nodesource.Source, error) {
	if grant == "" {
		return nil, nodesource.ErrNoGrant
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.refused[grant]; err != nil {
		return nil, err
	}
	source := newTestSource(grant)
	p.opened = append(p.opened, source)
	return source, nil
}

// refuse makes every later open of grant fail with err.
func (p *pathSources) refuse(grant string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refused == nil {
		p.refused = make(map[string]error)
	}
	p.refused[grant] = err
}

// open lists the sources opened and not yet closed or withdrawn.
func (p *pathSources) open() []*testSource {
	p.mu.Lock()
	defer p.mu.Unlock()
	var live []*testSource
	for _, source := range p.opened {
		if source.ctx.Err() == nil {
			live = append(live, source)
		}
	}
	return live
}

// sourcesOf is the opener a test server runs with.
func sourcesOf(s *Server) *pathSources { return s.opener.(*pathSources) }

// testSource is an open source at path.
type testSource struct {
	path   string
	ctx    context.Context //nolint:containedctx // the source's own lifetime, which Context exposes
	cancel context.CancelCauseFunc
}

func newTestSource(path string) *testSource {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &testSource{path: path, ctx: ctx, cancel: cancel}
}

func (s *testSource) Input() string            { return s.path }
func (s *testSource) Identity() string         { return "etag:" + s.path }
func (s *testSource) Size() int64              { return 0 }
func (s *testSource) ModTime() time.Time       { return time.Time{} }
func (s *testSource) InputModTime() time.Time  { return time.Time{} }
func (s *testSource) Context() context.Context { return s.ctx }
func (s *testSource) Err() error               { return context.Cause(s.ctx) }
func (s *testSource) Close() error             { s.cancel(nodesource.ErrClosed); return nil }

// fakeCore answers the node's resolve and revocation calls from what a test
// set.
type fakeCore struct {
	mu          sync.Mutex
	resolutions map[string]*nodecontrol.Resolution
	errs        map[string]error
	calls       []nodecontrol.ResolveRequest
	revokeErr   error
	revokes     int
}

func (c *fakeCore) Resolve(_ context.Context, req nodecontrol.ResolveRequest) (*nodecontrol.Resolution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, req)
	if err := c.errs[req.TransportID]; err != nil {
		return nil, err
	}
	resolution, ok := c.resolutions[req.TransportID]
	if !ok {
		return nil, &nodecontrol.StatusError{Path: nodecontrol.PathResolve, Status: http.StatusNotFound, Reason: nodecontrol.ResolveNotFound}
	}
	copied := *resolution
	return &copied, nil
}

func (c *fakeCore) RevokeRecipes(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revokes++
	return c.revokeErr
}

// resolve makes the core answer transportID with resolution.
func (c *fakeCore) resolve(transportID string, resolution *nodecontrol.Resolution) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolutions == nil {
		c.resolutions = make(map[string]*nodecontrol.Resolution)
	}
	c.resolutions[transportID] = resolution
}

// refuse makes the core answer transportID with err.
func (c *fakeCore) refuse(transportID string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errs == nil {
		c.errs = make(map[string]error)
	}
	c.errs[transportID] = err
}

func (c *fakeCore) resolveCalls() []nodecontrol.ResolveRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// coreOf is the core a test server asks, wiring one when it has none.
func coreOf(s *Server) *fakeCore {
	if s.core == nil {
		s.core = &fakeCore{}
	}
	return s.core.(*fakeCore)
}

// refusal is the core refusing a resolve with reason.
func refusal(status int, reason string) error {
	return &nodecontrol.StatusError{Path: nodecontrol.PathResolve, Status: status, Reason: reason}
}

// resolutionOf is the core's answer for card: its recipe, the playback
// identity it carries, and a grant for its input path.
func resolutionOf(card playback.RecipeCard) *nodecontrol.Resolution {
	return &nodecontrol.Resolution{
		Recipe: card, PlaybackSessionID: card.SessionID, UserID: card.UserID, ProfileID: card.ProfileID,
		MediaFileID: card.MediaFileID, SourceGrant: card.InputPath,
	}
}
