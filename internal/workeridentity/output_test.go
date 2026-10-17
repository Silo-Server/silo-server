package workeridentity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProxyOutputProofIsScopedAndReplayFenced(t *testing.T) {
	_, core, _ := ed25519.GenerateKey(rand.Reader)
	g := Grant{Version: 1, Domain: DomainProxyOutputGrant, Audience: "https://worker.example", NodeID: 7, WorkID: "work", Generation: 2, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	key, err := OutputPrivateKey("root-secret", g)
	if err != nil {
		t.Fatal(err)
	}
	changed := g
	changed.Generation++
	other, _ := OutputPrivateKey("root-secret", changed)
	if bytes.Equal(key, other) {
		t.Fatal("generation reused proxy authority")
	}
	g.Body, _ = json.Marshal(OutputAuthority{ProxyPublicKey: key.Public().(ed25519.PublicKey), Claims: json.RawMessage(`{"sid":"work"}`)})
	encoded, err := EncodeGrant(g, core)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "https://worker.example/transcode/work/master.m3u8", nil)
	req.Header.Set("X-Silo-Stream-Token", encoded)
	if err = Sign(req, g.Audience, DomainProxyOutputRequest, key, g.NodeID, false); err != nil {
		t.Fatal(err)
	}
	v := &Verifier{Origin: g.Audience, Domain: DomainProxyOutputRequest}
	if err = v.VerifyKey(req, 7, key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if err = v.VerifyKey(req, 7, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("replayed proxy proof")
	}
	fresh := req.Clone(t.Context())
	fresh.Header = req.Header.Clone()
	fresh.Header.Set("X-Silo-Stream-Token", "altered")
	if err = (&Verifier{Origin: g.Audience, Domain: DomainProxyOutputRequest}).VerifyKey(fresh, 7, key.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("proof did not bind output authority")
	}
}
