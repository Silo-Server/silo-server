package workercontrolcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type dispatchKey struct{}
type dispatchState struct {
	assignment, old workerwire.Assignment
	once            sync.Once
	err             error
	cancel          context.CancelFunc
}

const executePath = "/worker/execute"
const transcodeStartPath = "/transcode/start"
const preparePath = "/downloads/prepare"
const sourceMedia = "media"
const sourceTheme = "theme"

func (s *Service) nodeForRequest(r *http.Request) (Node, bool, error) {
	if r.URL == nil || r.URL.User != nil || r.URL.Fragment != "" {
		return Node{}, false, ErrDenied
	}
	base := r.URL.Scheme + "://" + r.URL.Host
	n, ok, err := s.options.Nodes.ByURL(r.Context(), base)
	if err != nil {
		return Node{}, false, err
	}
	if !ok {
		return Node{}, false, nil
	}
	canonical, err := workeridentity.ValidateOrigin(n.URL, n.AllowHTTP)
	if err != nil || !workeridentity.SameOrigin(canonical, base, n.AllowHTTP) || !n.Enabled || n.ID <= 0 || len(n.PublicKey) != 32 {
		return Node{}, false, ErrDenied
	}
	n.URL = canonical
	return n, true, nil
}
func requestWork(path string) string {
	p := strings.Split(strings.Trim(path, "/"), "/")
	if len(p) >= 2 && (p[0] == "transcode" || p[0] == "remux") {
		return p[1]
	}
	if len(p) >= 3 && p[0] == "downloads" && (p[1] == "artifacts" || p[1] == "prepare") {
		return p[2]
	}
	return ""
}
func bodyWork(body map[string]json.RawMessage) string {
	for _, k := range []string{"session_id", "job_id", "artifact_id"} {
		var v string
		if json.Unmarshal(body[k], &v) == nil && v != "" {
			return v
		}
	}
	return "job-" + uuid.NewString()
}

type input struct{ path, kind, placeholder string }

func inputKind(key, path string) string {
	switch key {
	case "input_path", "input", "source_path":
		if strings.Contains(path, sourceTheme) {
			return sourceTheme
		}
		return sourceMedia
	case "subtitle_path":
		return "subtitle"
	case "font_path":
		return "font"
	}
	return ""
}
func rewriteInputs(raw json.RawMessage, path string, inputs *[]input) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, ErrDenied
	}
	for k, v := range obj {
		kind := inputKind(k, path)
		if kind != "" {
			var p string
			if json.Unmarshal(v, &p) != nil || p == "" || strings.HasPrefix(p, "source:") {
				return nil, ErrDenied
			}
			alias := "source:" + uuid.NewString()
			*inputs = append(*inputs, input{p, kind, alias})
			obj[k], _ = json.Marshal(alias)
		} else if len(v) > 0 && v[0] == '{' {
			nested, err := rewriteInputs(v, path, inputs)
			if err != nil {
				return nil, err
			}
			obj[k] = nested
		} else if len(v) > 0 && v[0] == '[' {
			var list []json.RawMessage
			if err := json.Unmarshal(v, &list); err != nil {
				return nil, err
			}
			for i, value := range list {
				if len(value) > 0 && value[0] == '{' {
					next, err := rewriteInputs(value, path, inputs)
					if err != nil {
						return nil, err
					}
					list[i] = next
				}
			}
			obj[k], _ = json.Marshal(list)
		}
	}
	if len(*inputs) > 32 {
		return nil, ErrDenied
	}
	return json.Marshal(obj)
}
func replaceAliases(raw json.RawMessage, aliases map[string]string) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	for k, v := range obj {
		if coreExecutionEnvironment(k) {
			delete(obj, k)
			continue
		}
		var text string
		if json.Unmarshal(v, &text) == nil {
			if next, ok := aliases[text]; ok {
				obj[k], _ = json.Marshal(next)
			}
		} else if len(v) > 0 && v[0] == '{' {
			next, err := replaceAliases(v, aliases)
			if err != nil {
				return nil, err
			}
			obj[k] = next
		} else if len(v) > 0 && v[0] == '[' {
			var list []json.RawMessage
			_ = json.Unmarshal(v, &list)
			for i, value := range list {
				if len(value) > 0 && value[0] == '{' {
					next, err := replaceAliases(value, aliases)
					if err != nil {
						return nil, err
					}
					list[i] = next
				}
			}
			obj[k], _ = json.Marshal(list)
		}
	}
	return json.Marshal(obj)
}

// These locations and resolved filters belong to the core's execution
// environment. The worker uses its local operator settings and resolves its
// own filter from the frozen policy, mode and source facts.
func coreExecutionEnvironment(key string) bool {
	switch key {
	case "output_dir", "output_subdir", "hw_device", "ffmpeg_path", "ffprobe_path", "tone_map_filter":
		return true
	}
	return false
}
func inputDigest(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])
}

// InputBindings are core-only digests, never part of Assignment or signed wire
// recipes. They prevent a mutable recipe switching to a different source path
// without a new approved admission.
type InputBinder interface {
	BindInputs(context.Context, int, string, uint64, map[string]string) error
	InputBindings(context.Context, int, string, uint64) (map[string]string, error)
}

func (s *Service) admit(ctx context.Context, node Node, work, path string, body json.RawMessage) (a workerwire.Assignment, err error) {
	var inputs []input
	rewritten, err := rewriteInputs(body, path, &inputs)
	if err != nil || len(inputs) == 0 || s.options.Sources == nil {
		return a, ErrDenied
	}
	if kind, ok := ctx.Value(recipeSourceKindKey{}).(string); ok && kind == sourceTheme {
		for i := range inputs {
			if inputs[i].kind == sourceMedia {
				inputs[i].kind = sourceTheme
			}
		}
	}
	var sourceFacts struct {
		ToneMapSourceRevision tonemap.SourceRevision `json:"tone_map_source_revision"`
	}
	if err = json.Unmarshal(body, &sourceFacts); err != nil {
		return a, err
	}
	for _, in := range inputs {
		if in.kind == sourceMedia {
			if err = sourceFacts.ToneMapSourceRevision.ValidatePath(in.path); err != nil {
				return a, err
			}
		}
	}
	a, err = s.begin(ctx, workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: work, Method: http.MethodPost, Path: path, Body: rewritten})
	if err != nil {
		return a, err
	}
	if err = s.startAdmission(ctx, a); err != nil {
		s.cancelAdmission(context.WithoutCancel(ctx), a)
		return a, err
	}
	defer func() {
		if err != nil {
			s.cancelAdmission(context.WithoutCancel(ctx), a)
		}
	}()
	if s.options.Admit != nil {
		if err = s.options.Admit(ctx, node, a); err != nil {
			return a, err
		}
	}
	a.Sources = map[string]workerwire.Source{}
	aliases := map[string]string{}
	bindings := map[string]string{}
	for _, in := range inputs {
		var source workerwire.Source
		source, err = s.options.Sources.Acquire(ctx, node.ID, work, a.Generation, in.path, in.kind)
		if err != nil {
			return a, err
		}
		if source.NodeID != node.ID || source.WorkID != work || source.Generation != a.Generation || !validWork(source.Ref.ID) || source.Ref.Kind != in.kind || source.Ref.Revision == "" || source.Grant == "" || source.Origin != s.options.Origin || source.Size <= 0 || source.ETag == "" {
			err = ErrDenied
			return a, err
		}
		u, parseErr := url.Parse(source.URL)
		if parseErr != nil || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Scheme+"://"+u.Host != s.options.Origin || !strings.HasPrefix(u.Path, "/api/internal/workers/"+strconv.Itoa(node.ID)+"/sources/") {
			err = ErrDenied
			return a, err
		}
		alias := "source:" + source.Ref.ID
		aliases[in.placeholder] = alias
		a.Sources[alias] = source
		bindings[alias] = inputDigest(in.path)
	}
	a.Body, err = replaceAliases(a.Body, aliases)
	if err != nil {
		return a, err
	}
	if path == preparePath {
		var original downloadprepare.Request
		if err = json.Unmarshal(body, &original); err != nil || original.SourceIdentity != "" {
			err = ErrDenied
			return a, err
		}
		alias, ok := primarySource(a)
		if !ok {
			err = ErrDenied
			return a, err
		}
		var obj map[string]json.RawMessage
		_ = json.Unmarshal(a.Body, &obj)
		source := a.Sources[alias]
		obj["source_identity"], _ = json.Marshal(source.Ref.ID + ":" + source.Ref.Revision)
		a.Body, err = json.Marshal(obj)
		if err != nil {
			return a, err
		}
		var opaque downloadprepare.Request
		if err = json.Unmarshal(a.Body, &opaque); err != nil {
			return a, err
		}
		bindings[receiptOriginal] = original.ExecutionFingerprint()
		bindings[receiptWorker] = opaque.ExecutionFingerprint()
	}
	binder, ok := s.options.Store.(InputBinder)
	if !ok {
		err = ErrUnavailable
		return a, err
	}
	if err = binder.BindInputs(ctx, node.ID, work, a.Generation, bindings); err != nil {
		return a, err
	}
	err = s.options.Store.Complete(ctx, a)
	return a, err
}
func (s *Service) cancelAdmission(ctx context.Context, a workerwire.Assignment) {
	if cancel, ok := s.options.Store.(interface {
		Cancel(context.Context, int, string, uint64) error
	}); ok {
		_ = cancel.Cancel(ctx, a.NodeID, a.WorkID, a.Generation)
	}
	if s.options.Sources != nil {
		_ = s.options.Sources.Release(ctx, a.NodeID, a.WorkID, a.Generation)
	}
	s.clearAdmission(a)
	s.invalidateCached(a.NodeID, a.WorkID)
}
func (s *Service) PrepareRequest(r *http.Request) (*http.Request, error) {
	if state, ok := r.Context().Value(dispatchKey{}).(*dispatchState); ok {
		node, found, err := s.nodeForRequest(r)
		if err != nil || !found || node.ID != state.assignment.NodeID || r.Method != http.MethodPost || r.URL.Path != executePath || r.URL.RawQuery != "" {
			return nil, ErrDenied
		}
		return r, nil
	}
	node, ok, err := s.nodeForRequest(r)
	if err != nil {
		return nil, err
	}
	if !ok {
		return r, nil
	}
	out := r.Clone(r.Context())
	out.Header = r.Header.Clone()
	out.Header.Del("Authorization")
	for k := range out.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-silo-node-") {
			out.Header.Del(k)
		}
	}
	if r.Method == http.MethodPost && r.Body != nil && r.URL.Path != executePath {
		body, err := io.ReadAll(io.LimitReader(r.Body, (512<<10)+1))
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		if len(body) > 512<<10 {
			return nil, ErrDenied
		}
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		out.ContentLength = int64(len(body))
		if len(body) > 0 {
			var obj map[string]json.RawMessage
			if json.Unmarshal(body, &obj) != nil || obj == nil {
				return nil, ErrDenied
			}
			var found []input
			if _, err = rewriteInputs(body, r.URL.Path, &found); err != nil {
				return nil, err
			}
			if len(found) > 0 {
				if r.URL.RawQuery != "" {
					return nil, ErrDenied
				}
				work := bodyWork(obj)
				if !validWork(work) {
					return nil, ErrDenied
				}
				old, _, err := s.options.Store.Get(r.Context(), node.ID, work)
				if err != nil {
					return nil, err
				}
				a, err := s.admit(r.Context(), node, work, r.URL.Path, body)
				if err != nil {
					return nil, err
				}
				encoded, err := json.Marshal(a)
				if err != nil {
					s.cancelAdmission(context.WithoutCancel(r.Context()), a)
					return nil, err
				}
				u := *out.URL
				u.Path = executePath
				u.RawPath = ""
				u.RawQuery = ""
				out.URL = &u
				dispatchCtx, cancel := context.WithCancel(out.Context())
				out = out.WithContext(context.WithValue(dispatchCtx, dispatchKey{}, &dispatchState{assignment: a, old: old, cancel: cancel}))
				out.Body = io.NopCloser(bytes.NewReader(encoded))
				out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }
				out.ContentLength = int64(len(encoded))
				out.Header.Set("Content-Type", "application/json")
			}
		}
	}
	work := requestWork(r.URL.Path)
	if r.Method == http.MethodDelete && validWork(work) {
		generations, ok := s.options.Store.(GenerationStore)
		if !ok {
			return nil, ErrUnavailable
		}
		cutoff, err := generations.Generation(r.Context(), node.ID, work)
		if err != nil {
			return nil, err
		}
		if err = s.stopAt(r.Context(), node, work, cutoff); err != nil {
			return nil, err
		}
		out.Header.Set("X-Silo-Worker-Work-ID", work)
		out.Header.Set("X-Silo-Worker-Generation", strconv.FormatUint(cutoff, 10))
	}
	if r.Method == http.MethodPost && r.URL.Path == "/admin/force-reload" {
		if err = s.revoke(r.Context(), node); err != nil {
			return nil, err
		}
	}
	if err = workeridentity.Sign(out, node.URL, workeridentity.DomainCoreCommand, s.options.PrivateKey, node.ID, node.AllowHTTP); err != nil {
		if state, ok := out.Context().Value(dispatchKey{}).(*dispatchState); ok {
			s.cancelAdmission(context.WithoutCancel(r.Context()), state.assignment)
		}
		return nil, err
	}
	return out, nil
}

// FinishRequest commits a candidate only after successful worker execution.
// Transport hooks that call PrepareRequest directly must also call this method.
func (s *Service) FinishRequest(r *http.Request, response *http.Response, requestErr error) error {
	state, ok := r.Context().Value(dispatchKey{}).(*dispatchState)
	if !ok {
		if requestErr != nil {
			return requestErr
		}
		return s.bridgeArtifact(r, response)
	}
	state.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
		defer cancel()
		if requestErr != nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			state.err = requestErr
			s.cancelAdmission(ctx, state.assignment)
			state.cancel()
			return
		}
		if state.assignment.Path == transcodeStartPath && response.StatusCode != http.StatusAccepted {
			state.err = ErrDenied
			s.cancelAdmission(ctx, state.assignment)
			state.cancel()
			return
		}
		if state.assignment.Path == preparePath {
			state.err = s.verifyPreparedResponse(ctx, state.assignment, response, state.cancel)
			if state.err != nil {
				s.cancelAdmission(ctx, state.assignment)
				state.cancel()
				return
			}
		}
		state.err = s.options.Store.Activate(ctx, state.assignment.NodeID, state.assignment.WorkID, state.assignment.Generation)
		if state.err != nil {
			s.cancelAdmission(ctx, state.assignment)
			state.cancel()
			return
		}
		s.completeBody(response, state.assignment, state.cancel)
		if state.assignment.Path == preparePath && s.options.Sources != nil {
			state.err = s.options.Sources.Release(ctx, state.assignment.NodeID, state.assignment.WorkID, state.assignment.Generation)
			if state.err != nil {
				return
			}
		}
		if s.options.Sources != nil && state.old.Generation > 0 && state.old.Generation != state.assignment.Generation {
			state.err = s.options.Sources.Release(ctx, state.old.NodeID, state.old.WorkID, state.old.Generation)
		}
	})
	return state.err
}
func (s *Service) stop(ctx context.Context, node Node, work string) error {
	generations, ok := s.options.Store.(GenerationStore)
	if !ok {
		return ErrUnavailable
	}
	generation, err := generations.Generation(ctx, node.ID, work)
	if err != nil {
		return err
	}
	return s.stopAt(ctx, node, work, generation)
}
func (s *Service) stopAt(ctx context.Context, node Node, work string, generation uint64) error {
	deleteRecipe, err := s.captureRecipeDeletion(ctx, node, work)
	if err != nil {
		return err
	}
	old, _, err := s.options.Store.Get(ctx, node.ID, work)
	if err != nil {
		return err
	}
	candidate, _, err := s.options.Store.Candidate(ctx, node.ID, work)
	if err != nil {
		return err
	}
	generations, ok := s.options.Store.(GenerationStore)
	if !ok {
		return ErrUnavailable
	}
	if err = generations.StopIfGeneration(ctx, node.ID, work, generation); err != nil {
		return err
	}
	s.invalidateThrough(node.ID, work, generation)
	if deleteRecipe != nil {
		if err = deleteRecipe(ctx); err != nil {
			return err
		}
	}
	if s.options.Sources != nil {
		for _, a := range []workerwire.Assignment{old, candidate} {
			if a.Generation > 0 {
				if err = s.options.Sources.Release(ctx, node.ID, work, a.Generation); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (s *Service) revoke(ctx context.Context, node Node) error {
	active, err := s.options.Store.Active(ctx, node.ID)
	if err != nil {
		return err
	}
	pending, err := s.options.Store.Pending(ctx, node.ID)
	if err != nil {
		return err
	}
	for work := range pending {
		active[work] = pending[work]
	}
	for work := range active {
		if err = s.stop(ctx, node, work); err != nil {
			return err
		}
	}
	if s.options.RecipeStore != nil {
		return s.options.RecipeStore.RevokeNode(ctx, node.URL)
	}
	return nil
}

// EnsureRecipeAssignment admits lazy reconstruction only from current core
// recipe authority, never from token-carried encode parameters.
func (s *Service) EnsureRecipeAssignment(ctx context.Context, nodeID int, work string) (workerwire.Assignment, error) {
	node, ok, err := s.options.Nodes.ByID(ctx, nodeID)
	if err != nil || !ok || !node.Enabled || !validWork(work) {
		return workerwire.Assignment{}, ErrDenied
	}
	a, found, err := s.options.Store.Get(ctx, nodeID, work)
	if err != nil {
		return a, err
	}
	if found {
		return s.restoreSources(ctx, node, a)
	}
	if s.options.RecipeStore == nil {
		return a, ErrDenied
	}
	card, ok := s.options.RecipeStore.Get(ctx, work)
	if !ok || card == nil || !recipeNode(*card, node) {
		return a, ErrDenied
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		return a, err
	}
	a, err = s.admit(recipeAdmissionContext(context.WithValue(ctx, recoveryKey{}, uint64(0)), *card), node, work, transcodeStartPath, encoded)
	if err != nil {
		return a, err
	}
	if err = s.options.Store.Activate(ctx, nodeID, work, a.Generation); err != nil {
		s.cancelAdmission(context.WithoutCancel(ctx), a)
		return a, err
	}
	s.finishAdmission(a)
	return a, nil
}
func recipeNode(card playback.RecipeCard, node Node) bool {
	if card.RoutingExecutionNodeID != 0 && card.RoutingExecutionNodeID != node.ID {
		return false
	}
	if card.TranscodeNodeURL != "" {
		if !workeridentity.SameOrigin(card.TranscodeNodeURL, node.URL, node.AllowHTTP) {
			return false
		}
	}
	return card.RoutingExecutionNodeID == node.ID || workeridentity.SameOrigin(card.TranscodeNodeURL, node.URL, node.AllowHTTP)
}
func (s *Service) IsActive(ctx context.Context, nodeID int, work string, generation uint64) bool {
	return s.WorkActive(ctx, nodeID, work, generation)
}
