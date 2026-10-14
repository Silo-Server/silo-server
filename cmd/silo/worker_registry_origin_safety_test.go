package main

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

func TestWorkerRegistryOriginIgnoresMalformedSuffix(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{"HTTPS://WORKER.EXAMPLE.TEST:443/%ZZ", "https://worker.example.test"},
		{"http://worker.example.test:80/#%ZZ", "http://worker.example.test"},
		{"https://[2001:db8::1]:443/%ZZ", "https://[2001:db8::1]"},
		{"https://worker.example.test?value=%ZZ", "https://worker.example.test"},
		{"https://worker.example.test:invalid/%ZZ", ""},
		{"https://userinfo@worker.example.test/%ZZ", ""},
		{"https:///%ZZ", ""},
		{"worker.example.test/%ZZ", ""},
	} {
		t.Run(test.raw, func(t *testing.T) {
			if got := workerRegistryOrigin(test.raw); got != test.want {
				t.Fatalf("origin=%q want=%q", got, test.want)
			}
		})
	}
}

func TestWorkerRegistryUnclassifiableSnapshotRejectsCertificate(t *testing.T) {
	pool := startupGuardPool(t)
	if _, err := pool.Exec(t.Context(), `INSERT INTO stream_nodes VALUES(19,'https://worker.example.test:invalid/%ZZ','transcode',TRUE); INSERT INTO worker_http_nodes VALUES(19,decode(repeat('00',32),'hex'),FALSE)`); err != nil {
		t.Fatal(err)
	}
	registry := &coreWorkerRegistry{repo: workerregistration.New(pool, nil)}
	check := func(t *testing.T) {
		if err := registry.refresh(t.Context()); err == nil {
			t.Fatal("unclassifiable URL certified as safe legacy absence")
		}
		if _, found, err := registry.ByURL(t.Context(), "https://worker.example.test"); err == nil && !found {
			t.Fatal("unclassifiable URL allowed legacy dispatch")
		}
	}
	t.Run("current", check)
	if _, err := pool.Exec(t.Context(), "DELETE FROM worker_http_nodes WHERE node_id=19"); err != nil {
		t.Fatal(err)
	}
	t.Run("retained after restart", func(t *testing.T) { registry = &coreWorkerRegistry{repo: workerregistration.New(pool, nil)}; check(t) })
}
