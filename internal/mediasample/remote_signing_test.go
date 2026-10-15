package mediasample

import (
	"encoding/json"
	"errors"
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

	result, err := RemoteClient{}.Run(t.Context(), node.URL+RemotePath, nodeauthtest.SharedSecret(secret), Request{Input: "/media/movie.mkv"})
	if err != nil || result.Decoder != softwareDecoder {
		t.Fatalf("Run = %+v, %v; want the node's result for a signed request", result, err)
	}
}

// A node whose own mount does not show the approved source answers 409 in
// plain text: a fault of that node, so a preferring caller samples elsewhere.
func TestRemoteClientTreatsMountMismatchAsInfrastructure(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "local source access: this node's mount does not show the approved source", http.StatusConflict)
	}))
	t.Cleanup(node.Close)

	_, err := RemoteClient{}.Run(t.Context(), node.URL+RemotePath, nodeauthtest.SharedSecret("secret"), Request{SourceGrant: "grant"})
	remoteErr, ok := errors.AsType[*RemoteError](err)
	if !ok || remoteErr.Status != http.StatusConflict || !remoteErr.Infrastructure() || Classify(err).Permanent() {
		t.Fatalf("Run on a 409 = %v; want a non-permanent infrastructure RemoteError", err)
	}
}
