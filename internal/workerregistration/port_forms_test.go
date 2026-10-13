package workerregistration

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

func TestEnrolledCanonicalPortFormsAfterURLEdit(t *testing.T) {
	for _, raw := range []string{"https://worker.example.test:0443/", "https://worker.example.test:008443/", "https://worker.example.test:/"} {
		t.Run(raw, func(t *testing.T) {
			repo := registrationDB(t)
			canonical, err := workeridentity.ValidateOrigin(raw, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=17", raw); err != nil {
				t.Fatal(err)
			}
			ticket, err := repo.Issue(t.Context(), 17, "https://core.example.test", false)
			if err != nil {
				t.Fatal(err)
			}
			key, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.Redeem(t.Context(), ticket, key); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=17", raw); err != nil {
				t.Fatal(err)
			}
			node, found, err := repo.ByURL(t.Context(), canonical)
			if err != nil || !found || node.ID != 17 {
				t.Fatalf("valid stored origin %q canonicalized as %q is not classified as enrolled: found=%v id=%d err=%v", raw, canonical, found, node.ID, err)
			}
		})
	}
}
