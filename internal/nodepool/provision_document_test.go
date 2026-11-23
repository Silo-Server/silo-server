package nodepool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadNodeProvisioning(t *testing.T) {
	key7, err := GenerateNodeKey(7)
	if err != nil {
		t.Fatal(err)
	}
	key9, err := GenerateNodeKey(9)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte(key9+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	yamlFile := filepath.Join(dir, "nodes.yaml")
	yamlDoc := "apiVersion: 1\nnodes:\n  gpu-9:\n    url: http://gpu-9:8090\n    key: {fromFile: " + keyFile + "}\n  gpu-7:\n    url: http://gpu-7:8090\n    key: {fromEnv: GPU7_KEY}\n"
	if err := os.WriteFile(yamlFile, []byte(yamlDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}

	got, err := ReadNodeProvisioning(env(map[string]string{EnvTranscodeNodesFile: yamlFile, "GPU7_KEY": " " + key7 + " "}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != EnvTranscodeNodesFile || len(got.Nodes) != 2 ||
		got.Nodes[0] != (ProvisionedNode{ID: 7, Name: "gpu-7", URL: "http://gpu-7:8090", Key: key7}) ||
		got.Nodes[1] != (ProvisionedNode{ID: 9, Name: "gpu-9", URL: "http://gpu-9:8090", Key: key9}) {
		t.Fatalf("read %+v", got)
	}
	if got, err := ReadNodeProvisioning(env(map[string]string{EnvTranscodeNodes: "  "})); got != nil || err != nil {
		t.Fatalf("blank: %+v, %v", got, err)
	}

	inline := func(doc string) string {
		_, err := ReadNodeProvisioning(env(map[string]string{EnvTranscodeNodes: doc, "GPU7_KEY": key7, "OTHER_KEY": key7}))
		if err == nil {
			t.Fatalf("accepted %s", doc)
		}
		return err.Error()
	}
	for doc, want := range map[string]string{
		`{"apiVersion":2,"nodes":{}}`: "apiVersion",
		`{"apiVersion":1}`:            "nodes is required",
		`{"apiVersion":1,"nodes":{"a":{"url":"http://a","key":{"fromEnv":"GPU7_KEY"}},"b":{"url":"http://b","key":{"fromEnv":"OTHER_KEY"}}}}`: "names node 7",
		`{"apiVersion":1,"nodes":{"a":{"url":"http://a","key":{"fromEnv":"GPU7_KEY"}},"a":{"url":"http://b","key":{"fromEnv":"GPU7_KEY"}}}}`:  "duplicate",
		`{"apiVersion":1,"nodes":{"a":{"url":"gpu:8090","key":{"fromEnv":"GPU7_KEY"}}}}`:                                                      "url",
		`{"apiVersion":1,"nodes":{"a":{"url":"http://a","key":{"fromEnv":"GPU7_KEY","fromFile":"/x"}}}}`:                                      "fromFile",
		`{"apiVersion":1,"nodes":{"a":{"url":"http://a","key":{"fromEnv":"UNSET"}}}}`:                                                         "empty",
		`{"apiVersion":1,"nodes":{"a":{"url":"http://a","key":{"fromEnv":"GPU7_KEY"},"group":"x"}}}`:                                          "field other than",
		// Member names match exactly: "Nodes" is not "nodes".
		`{"apiVersion":1,"nodes":null,"Nodes":{}}`: "field other than",
		`{"apiVersion":1.0,"nodes":{}}`:            "apiVersion",
	} {
		if msg := inline(doc); !strings.Contains(msg, want) {
			t.Errorf("%s: %q does not mention %q", doc, msg, want)
		}
	}

	// A key written inline, where a reference belongs, is never echoed.
	if msg := inline(`{"apiVersion":1,"nodes":{"a":{"url":"http://a","key":"` + key7 + `"}}}`); strings.Contains(msg, key7) {
		t.Fatalf("the error quotes the key: %s", msg)
	}
	if msg := inline("apiVersion: 1\nnodes:\n  a: {url: http://a, key: " + key7 + "}\n"); strings.Contains(msg, key7) {
		t.Fatalf("the error quotes the key: %s", msg)
	}
	if msg := inline(`{"apiVersion":1,"nodes":{"a":{"url":"http://a","key":{"` + key7 + `":true}}}}`); strings.Contains(msg, key7) {
		t.Fatalf("the error quotes the key: %s", msg)
	}
	// yaml.v3 rounds this to 1.
	roundedFile := filepath.Join(dir, "rounded.yaml")
	if err := os.WriteFile(roundedFile, []byte("apiVersion: 1.00000000000000001\nnodes: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadNodeProvisioning(env(map[string]string{EnvTranscodeNodesFile: roundedFile})); err == nil {
		t.Fatal("accepted a fractional apiVersion")
	}
	if _, err := ReadNodeProvisioning(env(map[string]string{EnvTranscodeNodes: "{}", EnvTranscodeNodesFile: yamlFile})); err == nil {
		t.Fatal("accepted both variables")
	}
}
