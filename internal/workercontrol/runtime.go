// Package workercontrol runs a transcode executor using HTTP control and source
// authority. It has no database, cache or object-store client.
package workercontrol

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type Options struct {
	Binding                                     workeridentity.Binding
	Scratch, FFmpeg, FFprobe, HWAccel, HWDevice string
	MaxJobs                                     int
	MaxSourceReaders, MaxInputs                 int
	Transport                                   http.RoundTripper
}

//nolint:containedctx // Owns the worker process lifetime and cancels all background control loops during Close.
type Runtime struct {
	options       Options
	observations  *observations
	ctx           context.Context
	cancel        context.CancelFunc
	client        *http.Client
	server        *transcodenode.Server
	handler       http.Handler
	inputs        *workerinput.Adapter
	authority     *workerwire.LocalAuthority
	verifier      *workeridentity.Verifier
	proxyVerifier *workeridentity.Verifier
	config        *localConfig
	renewals      *sourceRenewals
	tracker       *reportTracker
	bootID        string
	sequence      uint64
	refreshMu     sync.Mutex
	refreshFlight *authorityRefresh
	refreshNext   time.Time
	refreshWake   chan struct{}
	mu            sync.Mutex
	generations   map[string]uint64
	handles       map[string]map[string]*workerinput.Handle
	tokens        map[string]tokenEntry
	staged        map[string]map[uint64]map[string]*workerinput.Handle
}

type tokenEntry struct {
	claims      *streamtoken.Claims
	workID      string
	generation  uint64
	expires     time.Time
	epoch       string
	authorityID string
}
type commandContextKey struct{}
type workContextKey struct{}
type generationContextKey struct{}

func New(ctx context.Context, options Options) (*Runtime, error) {
	if err := options.Binding.Validate(); err != nil {
		return nil, err
	}
	if options.Scratch == "" || !filepath.IsAbs(options.Scratch) {
		return nil, errors.New("worker scratch must be an absolute local directory")
	}
	if err := os.MkdirAll(options.Scratch, 0700); err != nil {
		return nil, err
	}
	if options.MaxJobs < 0 {
		return nil, errors.New("worker job ceiling cannot be negative")
	}
	runCtx, cancel := context.WithCancel(ctx)
	r := &Runtime{options: options, ctx: runCtx, cancel: cancel, bootID: nonce(), generations: make(map[string]uint64), handles: make(map[string]map[string]*workerinput.Handle), tokens: make(map[string]tokenEntry), staged: make(map[string]map[uint64]map[string]*workerinput.Handle)}
	r.renewals = newSourceRenewals(r)
	transport := options.Transport
	if transport == nil {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			cancel()
			return nil, errors.New("worker default HTTP transport unavailable")
		}
		t := base.Clone()
		t.Proxy = nil
		t.DisableCompression = true
		t.MaxIdleConnsPerHost = 32
		t.ResponseHeaderTimeout = 2 * time.Second
		transport = t
	}
	r.client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var err error
	r.authority, err = workerwire.NewLocalAuthority(options.Binding.NodeID, r.bootID, nonce(), func(work string, generation uint64) { go r.revoked(work, generation) })
	if err != nil {
		cancel()
		return nil, err
	}
	core, _ := url.Parse(options.Binding.CoreURL)
	coreOrigin := core.Scheme + "://" + core.Host
	r.inputs, err = workerinput.New(workerinput.Options{LocalRoot: options.Scratch, FFprobe: options.FFprobe, Origins: []string{coreOrigin}, AllowHTTP: options.Binding.AllowHTTP, Allowed: r.workAllowed, MaxReads: options.MaxSourceReaders, MaxInputs: options.MaxInputs, Sign: func(request *http.Request) error {
		return workeridentity.Sign(request, coreOrigin, workeridentity.DomainSourceRequest, options.Binding.NodePrivateKey, options.Binding.NodeID, options.Binding.AllowHTTP)
	}, Transport: transport})
	if err != nil {
		r.authority.Close()
		cancel()
		return nil, err
	}
	r.config = newLocalConfig(options)
	r.config.setRefresh(r.refresh)
	r.tracker = &reportTracker{runtime: r, sessions: make(map[string]nodesessions.SessionInfo)}
	r.verifier = &workeridentity.Verifier{Origin: options.Binding.NodeURL, Domain: workeridentity.DomainCoreCommand, AllowHTTP: options.Binding.AllowHTTP, ResolveKey: func(nodeID int) (ed25519.PublicKey, error) {
		if nodeID != options.Binding.NodeID {
			return nil, errors.New("wrong worker")
		}
		return options.Binding.CorePublicKey, nil
	}}
	r.proxyVerifier = &workeridentity.Verifier{Origin: options.Binding.NodeURL, Domain: workeridentity.DomainProxyOutputRequest, AllowHTTP: options.Binding.AllowHTTP}
	r.server, err = transcodenode.NewRuntimeServer(transcodenode.RuntimeOptions{Config: r.config, Tracker: r.tracker, VerifyCommand: r.verifyCommand, VerifyToken: r.verifyToken, Denied: r.denied, ExecuteHandler: http.HandlerFunc(r.execute)})
	if err != nil {
		_ = r.inputs.Close()
		r.authority.Close()
		cancel()
		return nil, err
	}
	r.server.SetInputPathAuthorizer(inputAuthorizer{r.inputs})
	r.server.SetThemeInputAuthorizer(inputAuthorizer{r.inputs})
	r.server.SetRecipeStore(recipeAdapter{r})
	if err := r.server.SetJobCeiling(options.MaxJobs); err != nil {
		_ = r.inputs.Close()
		r.authority.Close()
		cancel()
		return nil, err
	}
	r.initObservations()
	r.handler = r.server.Handler()
	return r, nil
}

func (r *Runtime) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		// Authenticate before Chi derives its route contexts. The output writer
		// must retain the verified scope through every clone of the request.
		if outputPathWork(request.URL.Path) != "" && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
			if err := r.verifyCommand(request); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if request.Method == http.MethodDelete && (strings.HasPrefix(request.URL.Path, "/transcode/") || strings.HasPrefix(request.URL.Path, "/remux/")) {
			r.serveStop(w, request)
			return
		}
		if request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/downloads/artifacts/") {
			r.serveArtifactStop(w, request)
			return
		}
		ctx := request.Context()
		if work := outputPathWork(request.URL.Path); work != "" {
			scope, _ := ctx.Value(outputRequestKey{}).(outputRequestAuthority)
			ctx = transcodenode.WithJobIdentity(ctx, executionJobIdentity(work, scope.jobID))
			ctx = context.WithValue(ctx, workContextKey{}, work)
		}
		if request.Header.Get(workeridentity.HeaderDomain) == workeridentity.DomainCoreCommand {
			if generation, err := strconv.ParseUint(request.Header.Get(workeridentity.HeaderGeneration), 10, 64); err == nil {
				ctx = context.WithValue(ctx, generationContextKey{}, generation)
			}
		}
		local := request.WithContext(workerinput.WithContext(ctx, r.inputs))
		if outputPathWork(local.URL.Path) != "" && (local.Method == http.MethodGet || local.Method == http.MethodHead) {
			w = &outputFencedWriter{ResponseWriter: w, runtime: r, request: local}
		}
		r.handler.ServeHTTP(w, local)
	})
}

func (r *Runtime) Run() {
	r.server.StartMetricsSampler(r.ctx)
	ready := r.server.StartHardwareEncoderWarmup(r.ctx)
	r.server.StartCapabilitySnapshots(r.ctx, ready)
	r.observations.registry.Start(r.ctx)
	go r.renewals.loop()
	go r.refreshLoop()
	go r.reportLoop()
	r.server.StartOrphanSweeper(r.ctx)
}

func (r *Runtime) Close(ctx context.Context) error {
	r.cancel()
	r.authority.Close()
	_ = r.inputs.Close()
	r.client.CloseIdleConnections()
	shutdownErr := r.server.Shutdown(ctx)
	return errors.Join(shutdownErr, r.observations.registry.Stop(ctx))
}

//nolint:gosec // Requests use the validated enrollment core origin and signed worker-control paths; redirects are disabled.
func (r *Runtime) callGrant(ctx context.Context, method, path string, body any, domain string, target any, scope ...*workeridentity.Grant) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	u := strings.TrimRight(r.options.Binding.CoreURL, "/") + "/api/internal/workers/" + strconv.Itoa(r.options.Binding.NodeID) + path
	request, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost && strings.HasPrefix(path, "/sources/") {
		grant, err := sourceRenewHeader(path)
		if err != nil {
			return err
		}
		request.Header.Set("X-Silo-Source-Grant", grant)
	}
	core, _ := url.Parse(r.options.Binding.CoreURL)
	if err := workeridentity.Sign(request, core.Scheme+"://"+core.Host, workeridentity.DomainNodeProof, r.options.Binding.NodePrivateKey, r.options.Binding.NodeID, r.options.Binding.AllowHTTP); err != nil {
		return err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return errControlUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 && response.StatusCode <= 599 {
			return fmt.Errorf("%w: HTTP%d", errControlUnavailable, response.StatusCode)
		}
		return fmt.Errorf("worker control returned %d", response.StatusCode)
	}
	var envelope struct {
		Grant string `json:"grant"`
	}
	// Complete the bounded HTTP read before decoding: a transport interruption
	// is retryable, while malformed JSON in a completed response is definitive.
	reply, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %w", errControlUnavailable, err)
	}
	if err := json.Unmarshal(reply, &envelope); err != nil {
		return err
	}
	grant, err := workeridentity.DecodeGrant(envelope.Grant, domain, r.options.Binding.NodeURL, r.options.Binding.CorePublicKey, time.Now())
	if err != nil || grant.NodeID != r.options.Binding.NodeID {
		return errors.New("invalid core control reply")
	}
	if len(scope) > 0 {
		*scope[0] = grant
	}
	return json.Unmarshal(grant.Body, target)
}

//nolint:nestif // Proxy output proofs and core commands retain separate refresh, viewer, and generation fences before attaching request authority.
func (r *Runtime) verifyCommand(request *http.Request) error {
	if request.Context().Value(commandContextKey{}) == r {
		return nil
	}
	if request.Header.Get(workeridentity.HeaderDomain) == workeridentity.DomainProxyOutputRequest {
		grant, body, claims, err := r.decodeOutput(request.Header.Get("X-Silo-Stream-Token"))
		if err != nil || !outputRouteAllowed(request, grant.WorkID) {
			return errors.New("proxy output scope denied")
		}
		if err := r.proxyVerifier.VerifyKey(request, r.options.Binding.NodeID, body.ProxyPublicKey); err != nil {
			return err
		}
		if !r.authority.OutputAllowed(body.AuthorityID) {
			if err := r.refresh(request.Context()); err != nil || !r.authority.OutputAllowed(body.AuthorityID) {
				return errors.New("viewer authority unavailable")
			}
		}
		if !r.workAllowed(grant.WorkID, grant.Generation) {
			if err := r.refresh(request.Context()); err != nil || !r.workAllowed(grant.WorkID, grant.Generation) {
				return errors.New("proxy output authority unavailable")
			}
		}
		r.mu.Lock()
		current := r.generations[grant.WorkID]
		r.mu.Unlock()
		if current != 0 && current != grant.Generation {
			return errors.New("proxy output generation changed")
		}
		ctx := context.WithValue(request.Context(), generationContextKey{}, grant.Generation)
		ctx = context.WithValue(ctx, outputRequestKey{}, outputRequestAuthority{work: grant.WorkID, jobID: body.JobID, generation: grant.Generation, epoch: body.Epoch, authorityID: body.AuthorityID, expires: claims.ExpiresAt.Time})
		ctx = context.WithValue(ctx, commandContextKey{}, r)
		*request = *request.WithContext(ctx)
		return nil
	}
	if err := r.verifier.Verify(request, r.options.Binding.NodeID); err != nil {
		return err
	}
	if work := outputPathWork(request.URL.Path); work != "" && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
		generation := r.authority.Generation(work)
		if generation == 0 {
			if strings.HasPrefix(request.URL.Path, "/downloads/") {
				_ = r.refresh(request.Context())
			} else {
				_, _ = r.currentRecipe(request.Context(), work)
			}
			generation = r.authority.Generation(work)
		}
		if generation == 0 || !r.workAllowed(work, generation) {
			return errors.New("core output authority unavailable")
		}
		ctx := context.WithValue(request.Context(), generationContextKey{}, generation)
		ctx = context.WithValue(ctx, outputRequestKey{}, outputRequestAuthority{work: work, generation: generation})
		*request = *request.WithContext(ctx)
	}
	*request = *request.WithContext(context.WithValue(request.Context(), commandContextKey{}, r))
	return nil
}

func (r *Runtime) execute(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if request.Header.Get(workeridentity.HeaderDomain) != workeridentity.DomainCoreCommand {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err := r.verifyCommand(request); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var assignment workerwire.Assignment
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&assignment); err != nil || assignment.Version != 1 || assignment.NodeID != r.options.Binding.NodeID || !workerwire.ValidExecutionID(assignment.WorkID) || assignment.JobID != "" && !workerwire.ValidExecutionID(assignment.JobID) || assignment.Generation == 0 || !strings.HasPrefix(assignment.Path, "/") || strings.Contains(assignment.Path, "?") || assignment.Path == "/worker/execute" {
		http.Error(w, "invalid assignment", http.StatusBadRequest)
		return
	}
	if err := r.refresh(request.Context()); err != nil || !r.workAllowed(assignment.WorkID, assignment.Generation) {
		http.Error(w, "assignment authority unavailable", http.StatusServiceUnavailable)
		return
	}
	r.mu.Lock()
	if old := r.generations[assignment.WorkID]; old > assignment.Generation {
		r.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		return
	}
	r.mu.Unlock()
	handles, err := r.stageSources(assignment)
	if err != nil {
		http.Error(w, "invalid assigned sources", http.StatusBadRequest)
		return
	}
	published := false
	defer func() {
		if !published {
			r.discardStaged(assignment.WorkID, assignment.Generation)
		}
	}()
	body, err := rewriteInputs(assignment.Body, handles)
	if err != nil {
		http.Error(w, "invalid assigned inputs", http.StatusBadRequest)
		return
	}
	jobCtx := transcodenode.WithJobIdentity(request.Context(), executionJobIdentity(assignment.WorkID, assignment.JobID))
	jobCtx = context.WithValue(jobCtx, workContextKey{}, assignment.WorkID)
	jobCtx = context.WithValue(jobCtx, generationContextKey{}, assignment.Generation)
	local, err := http.NewRequestWithContext(workerinput.WithContext(context.WithValue(jobCtx, commandContextKey{}, r), r.inputs), assignment.Method, assignment.Path, bytes.NewReader(body))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	local.Header = request.Header.Clone()
	local.Header.Set("Content-Type", "application/json")
	response := &executionResponse{ResponseWriter: w}
	r.handler.ServeHTTP(response, local)
	if response.status >= 200 && response.status < 300 && r.workAllowed(assignment.WorkID, assignment.Generation) {
		if assignment.Path == "/downloads/prepare" {
			r.completeSourceWork(assignment.WorkID, assignment.Generation, handles)
			published = true
			return
		}
		r.publishStaged(assignment.WorkID, assignment.Generation)
		published = true
	}
}

func (r *Runtime) stageSources(assignment workerwire.Assignment) (map[string]*workerinput.Handle, error) {
	if r.renewals.blocked(assignment.WorkID, assignment.Generation) {
		return nil, errors.New("worker source lease fenced")
	}
	r.mu.Lock()
	if existing := r.handles[assignment.WorkID]; r.generations[assignment.WorkID] == assignment.Generation && liveSourceHandles(existing) {
		r.mu.Unlock()
		return existing, nil
	}
	if existing := r.staged[assignment.WorkID][assignment.Generation]; liveSourceHandles(existing) {
		r.mu.Unlock()
		return existing, nil
	}
	var retired []map[string]*workerinput.Handle
	if r.generations[assignment.WorkID] == assignment.Generation {
		retired = append(retired, r.handles[assignment.WorkID])
		delete(r.handles, assignment.WorkID)
	}
	if existing := r.takeStagedLocked(assignment.WorkID, assignment.Generation); existing != nil {
		retired = append(retired, existing)
	}
	r.mu.Unlock()
	for _, handles := range retired {
		closeHandles(handles)
	}
	opened := make(map[string]*workerinput.Handle)
	for name, source := range assignment.Sources {
		if name != "source:"+source.Ref.ID || source.NodeID != assignment.NodeID || source.WorkID != assignment.WorkID || source.Generation != assignment.Generation {
			closeHandles(opened)
			return nil, errors.New("source scope mismatch")
		}
		h, err := r.inputs.Open(r.ctx, workerinput.Source{ID: source.Ref.ID, Revision: source.Ref.Revision, URL: source.URL, Grant: source.Grant, ETag: source.ETag, Size: source.Size, WorkID: source.WorkID, Generation: source.Generation})
		if err != nil {
			closeHandles(opened)
			return nil, err
		}
		opened[name] = h
	}
	if err := r.trackSourceRenewals(assignment.Sources, opened); err != nil {
		closeHandles(opened)
		return nil, err
	}
	r.mu.Lock()
	// Another reconstruction may have published this generation while its
	// redundant handles were opening. Keep the input already used by callers.
	if r.generations[assignment.WorkID] == assignment.Generation && liveSourceHandles(r.handles[assignment.WorkID]) {
		result := r.handles[assignment.WorkID]
		r.mu.Unlock()
		closeHandles(opened)
		return result, nil
	}
	if r.staged[assignment.WorkID] == nil {
		r.staged[assignment.WorkID] = make(map[uint64]map[string]*workerinput.Handle)
	}
	if existing := r.staged[assignment.WorkID][assignment.Generation]; liveSourceHandles(existing) {
		r.mu.Unlock()
		closeHandles(opened)
		return existing, nil
	}
	old := r.staged[assignment.WorkID][assignment.Generation]
	r.staged[assignment.WorkID][assignment.Generation] = opened
	r.mu.Unlock()
	closeHandles(old)
	return opened, nil
}

// takeStagedLocked retires only the requested generation and its empty parent.
// The caller must hold r.mu.
func (r *Runtime) takeStagedLocked(work string, generation uint64) map[string]*workerinput.Handle {
	handles := r.staged[work][generation]
	delete(r.staged[work], generation)
	if len(r.staged[work]) == 0 {
		delete(r.staged, work)
	}
	return handles
}

func (r *Runtime) publishStaged(work string, generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	handles := r.takeStagedLocked(work, generation)
	if handles == nil {
		return
	}
	if current := r.generations[work]; current > generation {
		closeHandles(handles)
		return
	}
	old := r.handles[work]
	r.handles[work] = handles
	r.generations[work] = generation
	r.tracker.retireGenerationLocked(work, generation, true)
	closeHandles(old)
}
func (r *Runtime) discardStaged(work string, generation uint64) {
	r.mu.Lock()
	handles := r.takeStagedLocked(work, generation)
	r.mu.Unlock()
	closeHandles(handles)
}
func (r *Runtime) openSources(assignment workerwire.Assignment) (map[string]*workerinput.Handle, error) {
	handles, err := r.stageSources(assignment)
	if err == nil {
		r.publishStaged(assignment.WorkID, assignment.Generation)
	}
	return handles, err
}
func liveSourceHandles(handles map[string]*workerinput.Handle) bool {
	if handles == nil {
		return false
	}
	for _, h := range handles {
		if h == nil || h.Context().Err() != nil {
			return false
		}
	}
	return true
}

func closeHandles(handles map[string]*workerinput.Handle) {
	for _, h := range handles {
		_ = h.Close()
	}
}

type executionResponse struct {
	http.ResponseWriter
	status int
}

func (w *executionResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *executionResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *executionResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func rewriteInputs(raw json.RawMessage, handles map[string]*workerinput.Handle) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var walk func(any) (any, error)
	walk = func(value any) (any, error) {
		switch typed := value.(type) {
		case map[string]any:
			if identity, exists := typed["source_identity"]; exists {
				input, ok := typed[inputPathField].(string)
				h := handles[input]
				if !ok || h == nil || identity != h.Identity() {
					return nil, errors.New("prepared source identity mismatch")
				}
			}
			for key, child := range typed {
				if text, ok := child.(string); ok && (key == inputPathField || key == "input" || key == "media_path") {
					h := handles[text]
					if h == nil {
						return nil, errors.New("unregistered source input")
					}
					typed[key] = h.URL()
					continue
				}
				updated, err := walk(child)
				if err != nil {
					return nil, err
				}
				typed[key] = updated
			}
		case []any:
			for i, child := range typed {
				updated, err := walk(child)
				if err != nil {
					return nil, err
				}
				typed[i] = updated
			}
		}
		return value, nil
	}
	updated, err := walk(body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(updated)
}

func (r *Runtime) denied(ctx context.Context, workID string) bool {
	r.mu.Lock()
	generation := r.generations[workID]
	r.mu.Unlock()
	if generation == 0 {
		if _, err := r.currentRecipe(ctx, workID); err != nil {
			return true
		}
		r.mu.Lock()
		generation = r.generations[workID]
		r.mu.Unlock()
	}
	return !r.workAllowed(workID, generation)
}

func (r *Runtime) revoked(workID string, generation uint64) {
	r.renewals.revoke(workID, generation)
	r.mu.Lock()
	pending := r.takeStagedLocked(workID, generation)
	var active map[string]*workerinput.Handle
	if r.generations[workID] == generation {
		active = r.handles[workID]
		delete(r.generations, workID)
		delete(r.handles, workID)
	}
	r.tracker.retireGenerationLocked(workID, generation, false)
	r.mu.Unlock()
	closeHandles(pending)
	paths := make([]string, 0, len(active))
	for _, h := range active {
		paths = append(paths, h.URL())
	}
	closeHandles(active)
	if r.server != nil && len(paths) > 0 {
		r.server.StopWorkInputs(r.ctx, workID, paths)
	}
}

func nonce() string { var raw [24]byte; _, _ = rand.Read(raw[:]); return hex.EncodeToString(raw[:]) }
