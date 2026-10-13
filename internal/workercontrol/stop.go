package workercontrol

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerinput"
)

type coreStopContextKey struct{}
type coreStopScope struct {
	runtime *Runtime
	work    string
}

func (r *Runtime) coreStopped(ctx context.Context, work string) bool {
	scope, ok := ctx.Value(coreStopContextKey{}).(coreStopScope)
	return ok && scope.runtime == r && scope.work == work
}

func (r *Runtime) serveStop(w http.ResponseWriter, request *http.Request) {
	if err := r.verifyCommand(request); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	work := outputPathWork(request.URL.Path)
	encoded := request.Header.Get(workeridentity.HeaderGeneration)
	cutoff, err := strconv.ParseUint(encoded, 10, 64)
	if err != nil || strconv.FormatUint(cutoff, 10) != encoded || work == "" || request.Header.Get(workeridentity.HeaderWorkID) != work {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	current := r.generations[work]
	generations := make(map[uint64]bool)
	var inputs []string
	if current != 0 && current <= cutoff {
		generations[current] = true
		for _, handle := range r.handles[work] {
			inputs = append(inputs, handle.URL())
		}
	}
	for generation, handles := range r.staged[work] {
		if generation <= cutoff {
			generations[generation] = true
			for _, handle := range handles {
				inputs = append(inputs, handle.URL())
			}
		}
	}
	r.mu.Unlock()
	// PrepareRequest has already revoked this exact core cutoff. The engine
	// must compare the captured inputs under its lifecycle lock: a newer
	// producer can publish while this HTTP request waits for that lock.
	ctx := transcodenode.WithJobIdentity(request.Context(), work)
	ctx = context.WithValue(ctx, workContextKey{}, work)
	ctx = context.WithValue(ctx, generationContextKey{}, cutoff)
	ctx = context.WithValue(ctx, coreStopContextKey{}, coreStopScope{r, work})
	ctx = transcodenode.WithStopInputScope(ctx, work, inputs)
	local := request.WithContext(workerinput.WithContext(ctx, r.inputs))
	response := &stopResponse{header: make(http.Header)}
	r.handler.ServeHTTP(response, local)
	for generation := range generations {
		r.revoked(work, generation)
	}
	// Stop responses are small control messages. Publish the acknowledgement
	// only after both the engine and captured source handles have retired.
	for key, values := range response.header {
		w.Header()[key] = values
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if response.body.Len() > 0 {
		_, _ = w.Write(response.body.Bytes())
	}
}

type stopResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *stopResponse) Header() http.Header { return w.header }
func (w *stopResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *stopResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	const maxStopBody = 4096
	remaining := maxStopBody - w.body.Len()
	if len(body) > remaining {
		_, _ = w.body.Write(body[:remaining])
		return remaining, io.ErrShortWrite
	}
	return w.body.Write(body)
}
