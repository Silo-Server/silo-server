package workercontrolcore

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func (s *Service) Handler() http.Handler {
	return sealedHandler{h: s.router()}
}

type sealedHandler struct{ h http.Handler }

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

// router is the sealed core-control registration surface, inventoried beside
// the API's exact gateway paths. It cannot escape through Handler.
func (s *Service) router() chi.Router {
	mux := chi.NewRouter()
	mux.With(workerPathValues).Post("/api/internal/workers/{node}/snapshot", s.snapshot)
	mux.With(workerPathValues).Post("/api/internal/workers/{node}/report", s.report)
	mux.With(workerPathValues).Get("/api/internal/workers/{node}/recipes/{work}", s.recipe)       //nolint:goconst // Route inventory requires literal registrations.
	mux.With(workerPathValues).Head("/api/internal/workers/{node}/recipes/{work}", s.recipe)      //nolint:goconst // Route inventory requires literal registrations.
	mux.With(workerPathValues).Delete("/api/internal/workers/{node}/recipes/{work}", s.closeWork) //nolint:goconst // Route inventory requires literal registrations.
	mux.With(workerPathValues).Post("/api/internal/workers/{node}/revoke", s.closeNode)
	mux.With(workerPathValues).Post("/api/internal/workers/{node}/sources/{grant}/renew", s.renew)
	if s.options.SourceHandler != nil {
		mux.With(workerPathValues).Method("GET", "/api/internal/workers/{node}/sources/{grant}", s.options.SourceHandler)
		mux.With(workerPathValues).Method("HEAD", "/api/internal/workers/{node}/sources/{grant}", s.options.SourceHandler)
	}
	return mux
}

func workerPathValues(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{protocolNodeParam, protocolWorkParam, protocolGrantParam} {
			r.SetPathValue(name, chi.URLParam(r, name))
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Service) authenticate(w http.ResponseWriter, r *http.Request) (Node, bool) {
	id, err := strconv.Atoi(r.PathValue("node"))
	if err != nil || id <= 0 || strconv.Itoa(id) != r.PathValue("node") || s.VerifyRequest(r, id, workeridentity.DomainNodeProof) != nil {
		http.Error(w, "worker proof required", http.StatusUnauthorized)
		return Node{}, false
	}
	node, ok, err := s.options.Nodes.ByID(r.Context(), id)
	if err != nil || !ok || !node.Enabled {
		http.Error(w, "worker authority unavailable", http.StatusUnauthorized)
		return Node{}, false
	}
	return node, true
}
func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		http.Error(w, "invalid control body", http.StatusBadRequest)
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, "invalid trailing control body", http.StatusBadRequest)
		return false
	}
	return true
}
func (s *Service) reply(w http.ResponseWriter, node Node, domain, work string, generation uint64, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "worker authority unavailable", http.StatusServiceUnavailable)
		return
	}
	grant, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: domain, Audience: node.URL, NodeID: node.ID, WorkID: work, Generation: generation, ExpiresAt: time.Now().Add(2 * time.Second).Unix(), Body: raw}, s.options.PrivateKey)
	if err != nil {
		http.Error(w, "worker authority unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(GrantReply{Grant: grant})
}
func (s *Service) snapshot(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		BootID   string `json:"boot_id"`
		Nonce    string `json:"nonce"`
		Sequence uint64 `json:"sequence"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if input.BootID == "" || len(input.BootID) > 160 || input.Nonce == "" || len(input.Nonce) > 160 || input.Sequence == math.MaxUint64 {
		http.Error(w, "invalid challenge", http.StatusBadRequest)
		return
	}
	snapshot, err := s.options.Store.Snapshot(r.Context(), node.ID)
	if err != nil {
		http.Error(w, "worker authority unavailable", http.StatusServiceUnavailable)
		return
	}
	active := map[string]uint64{}
	pending := map[string]uint64{}
	for work, a := range snapshot.Active {
		if s.executionActive(r.Context(), node, a) {
			active[work] = a.Generation
		}
	}
	for work, a := range snapshot.Pending {
		if s.pendingOwned(r.Context(), node.ID, work, a.Generation) {
			pending[work] = a.Generation
		}
	}
	var executionConfig *workerwire.ExecutionConfig
	if s.options.ExecutionConfig != nil {
		settings, err := s.options.ExecutionConfig(r.Context(), node.ID)
		if err != nil || settings.Validate() != nil {
			http.Error(w, "worker execution settings unavailable", http.StatusServiceUnavailable)
			return
		}
		executionConfig = &settings
	}
	outputs, err := s.preparedOutputs(r.Context(), node)
	if err != nil {
		http.Error(w, "worker output authorities unavailable", http.StatusServiceUnavailable)
		return
	}
	s.reply(w, node, workeridentity.DomainSnapshotGrant, "", 0, workerwire.Snapshot{Outputs: outputs, Version: 1, NodeID: node.ID, BootID: input.BootID, Nonce: input.Nonce, Sequence: input.Sequence + 1, Active: active, Pending: pending, ValidForMillis: 2000, ExecutionConfig: executionConfig})
}
func (s *Service) report(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil || !json.Valid(raw) {
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	if s.options.Report != nil {
		if err = s.options.Report(r.Context(), node.ID, raw); err != nil {
			http.Error(w, "report unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	s.reply(w, node, "silo/worker-report/v1", "", 0, struct {
		Accepted bool `json:"accepted"`
	}{true})
}
func primarySource(a workerwire.Assignment) (string, bool) {
	var input struct {
		InputPath  string `json:"input_path"`
		Input      string `json:"input"`
		SourcePath string `json:"source_path"`
	}
	if json.Unmarshal(a.Body, &input) != nil {
		return "", false
	}
	for _, alias := range []string{input.InputPath, input.Input, input.SourcePath} {
		if source, ok := a.Sources[alias]; ok && (source.Ref.Kind == sourceMedia || source.Ref.Kind == sourceTheme) {
			return alias, true
		}
	}
	return "", false
}
func (s *Service) currentRecipe(r *http.Request, node Node, work string) (RecipeReply, error) {
	if !validWork(work) {
		return RecipeReply{}, ErrDenied
	}
	a, err := s.EnsureRecipeAssignment(r.Context(), node.ID, work)
	if err != nil {
		return RecipeReply{}, err
	}
	if !s.executionActive(r.Context(), node, a) {
		return RecipeReply{}, ErrDenied
	}
	var card playback.RecipeCard
	if s.options.RecipeStore != nil {
		latest, ok := s.options.RecipeStore.Get(r.Context(), work)
		if ok && latest != nil {
			if !recipeNode(*latest, node) {
				return RecipeReply{}, ErrDenied
			}
			card = *latest
		} else if !s.HasInFlight(node.ID, work) {
			return RecipeReply{}, ErrDenied
		} else if err = json.Unmarshal(a.Body, &card); err != nil {
			return RecipeReply{}, err
		}
	} else if err = json.Unmarshal(a.Body, &card); err != nil {
		return RecipeReply{}, err
	}
	alias, ok := primarySource(a)
	if !ok {
		return RecipeReply{}, ErrDenied
	}
	if !strings.HasPrefix(card.InputPath, "source:") {
		binder, ok := s.options.Store.(InputBinder)
		if !ok {
			return RecipeReply{}, ErrUnavailable
		}
		bindings, err := binder.InputBindings(r.Context(), node.ID, work, a.Generation)
		if err != nil {
			return RecipeReply{}, err
		}
		if bindings[alias] != inputDigest(card.InputPath) {
			return RecipeReply{}, ErrDenied
		}
	}
	card.InputPath = alias
	card.TranscodeNodeURL = node.URL
	card.RoutingExecutionNodeID = node.ID
	// Core filesystem locations may also appear in output/environment fields.
	card.OutputSubdir = ""
	card.HWDevice = ""
	card.ToneMapFilter = ""
	a.Body, err = replaceAliases(a.Body, nil)
	if err != nil {
		return RecipeReply{}, err
	}
	return RecipeReply{Assignment: a, Recipe: card}, nil
}
func (s *Service) recipe(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	reply, err := s.currentRecipe(r, node, r.PathValue("work"))
	if err != nil {
		http.Error(w, "current recipe unavailable", http.StatusForbidden)
		return
	}
	s.reply(w, node, "silo/worker-recipe/v1", reply.Assignment.WorkID, reply.Assignment.Generation, reply)
}
func tokenWork(claims *streamtoken.Claims) string {
	if claims.DownloadArtifactID != "" {
		return claims.DownloadArtifactID
	}
	if claims.TranscodeTransportID != "" {
		return claims.TranscodeTransportID
	}
	if claims.SessionID != "" {
		return claims.SessionID
	}
	return ""
}
func (s *Service) closeWork(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	work := r.PathValue("work")
	var input struct {
		Generation uint64 `json:"generation"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if !validWork(work) || s.stopAt(r.Context(), node, work, input.Generation) != nil {
		http.Error(w, "close unavailable", http.StatusServiceUnavailable)
		return
	}
	s.reply(w, node, "silo/worker-close/v1", work, 0, struct {
		Accepted bool `json:"accepted"`
	}{true})
}
func (s *Service) closeNode(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if s.revoke(r.Context(), node) != nil {
		http.Error(w, "close unavailable", http.StatusServiceUnavailable)
		return
	}
	s.reply(w, node, "silo/worker-close/v1", "", 0, struct {
		Accepted bool `json:"accepted"`
	}{true})
}
func (s *Service) renew(w http.ResponseWriter, r *http.Request) {
	node, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	grantID := r.PathValue("grant")
	if r.Header.Get("X-Silo-Source-Grant") != grantID || grantID == "" {
		http.Error(w, "source grant mismatch", http.StatusForbidden)
		return
	}
	var input struct {
		WorkID     string `json:"work_id"`
		Generation uint64 `json:"generation"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if !s.WorkActive(r.Context(), node.ID, input.WorkID, input.Generation) {
		http.Error(w, "source authority denied", http.StatusForbidden)
		return
	}
	authority, ok := s.options.Sources.(interface {
		Renew(context.Context, int, string, uint64, string) (workerwire.Source, error)
	})
	if !ok {
		http.Error(w, "renewal unavailable", http.StatusServiceUnavailable)
		return
	}
	source, err := authority.Renew(r.Context(), node.ID, input.WorkID, input.Generation, grantID)
	if err != nil {
		http.Error(w, "source renewal denied", http.StatusForbidden)
		return
	}
	s.reply(w, node, workeridentity.DomainSourceGrant, input.WorkID, input.Generation, source)
}
