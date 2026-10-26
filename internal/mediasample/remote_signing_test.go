package mediasample

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

func TestRemoteClientSignsTheRequest(t *testing.T) {
	const secret = "sample-secret"
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != RemotePath || !nodeauthtest.Signed(r, secret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(Result{Decoder: softwareDecoder})
	}))
	t.Cleanup(node.Close)

	result, err := RemoteClient{}.Run(t.Context(), node.URL+RemotePath, secret, Request{Input: "/media/movie.mkv"})
	if err != nil || result.Decoder != softwareDecoder {
		t.Fatalf("Run = %+v, %v; want the node's result for a signed request", result, err)
	}
}
