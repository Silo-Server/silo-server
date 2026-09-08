package transcodeproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAcknowledgeSurvivesCompletedDownstreamRequest(t *testing.T) {
	const (
		jwtSecret  = "test-secret"
		generation = "incarnation:generation"
	)

	acknowledged := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+jwtSecret {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get(GenerationHeader); got != generation {
			t.Errorf("generation = %q, want %q", got, generation)
		}
		acknowledged <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if err := Acknowledge(requestCtx, server.Client(), server.URL+"/segment", jwtSecret, generation); err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}
	select {
	case <-acknowledged:
	default:
		t.Fatal("acknowledgement request was not delivered")
	}
}
