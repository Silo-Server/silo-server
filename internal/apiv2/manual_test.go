package apiv2

import (
	"testing"
)

func TestReconcileSpecIgnoresPathItemMetadata(t *testing.T) {
	doc := []byte(`{"paths":{"/api/v2/artwork/{key}":{
		"summary":"Artwork", "description":"Signed bytes",
		"servers":[{"url":"https://example.invalid"}],
		"parameters":[{"in":"path","name":"key","required":true}],
		"x-other-extension":["ignored"],
		"get":{"x-silo-wildcard-param":"key"},
		"head":{"x-silo-wildcard-param":"key"}
	}}}`)
	unaccounted, unserved, err := reconcileSpec([]string{"GET /api/v2/artwork/*", "HEAD /api/v2/artwork/*"}, doc)
	if err != nil || len(unaccounted) != 0 || len(unserved) != 0 {
		t.Fatalf("unaccounted=%v unserved=%v err=%v", unaccounted, unserved, err)
	}
}
