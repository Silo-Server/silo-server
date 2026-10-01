package telemetry

import (
	"errors"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func nodeOperation(operation string) string {
	switch operation {
	case "capabilities", "transcode_start", "stream", "stream_ack", "chapter_extract", "reload", "reprobe", "status":
		return operation
	default:
		return "other"
	}
}

// DoTrustedNode records an authenticated call to a configured Silo worker.
// Redirects are refused: neither credentials nor correlation headers may follow
// a worker-supplied redirect. Response bodies retain their streaming behavior.
// Latency measures response headers, including the node's synchronous work.
func DoTrustedNode(client *http.Client, req *http.Request, operation string) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, finish := StartDependency(req.Context(), "node", "worker", nodeOperation(operation))
	copy := req.Clone(ctx)
	InjectTrusted(ctx, copy.Header)
	isolated := *client
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := isolated.Do(copy)
	observedErr := err
	if err == nil && resp.StatusCode >= 400 {
		observedErr = errors.New("node response error")
	}
	finish(observedErr)
	return resp, err
}

// TrustedHTTPHandler must run after internal bearer authentication. Public
// stream credentials do not establish this trust. The server span uses a fixed
// role; request paths, headers and stream/session identities are not recorded.
func TrustedHTTPHandler(role string, next http.Handler) http.Handler {
	role = Role(role)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer("silo/nodes").Start(ExtractTrusted(r.Context(), r.Header), "node."+role,
			trace.WithSpanKind(trace.SpanKindServer))
		if span.IsRecording() {
			span.SetAttributes(attribute.String("role", role))
		}
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
