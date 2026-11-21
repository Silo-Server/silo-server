package main

import (
	"encoding/xml"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/joho/godotenv"
)

// The node.env the installer writes is one the node starts from, with the key
// read from node-key, even under a home whose path needs quoting.
func TestRenderNodeEnvStartsTheNode(t *testing.T) {
	key := nodeKeyFor(t, testTranscodeNodeID)
	paths := nodeInstallPathsIn(filepath.Join(t.TempDir(), "home $USER #1"))
	if err := os.MkdirAll(paths.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.key, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := nodeInstallSettings{server: "http://localhost:8090", key: key, port: 8100}
	env, err := renderNodeEnv(settings, paths.key, nil)
	if err != nil {
		t.Fatal(err)
	}
	values, err := godotenv.Unmarshal(string(env))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"MODE": serverModeTranscode, envCoreURL: settings.server, "SILO_NODE_KEY_FILE": paths.key, "PORT": "8100"}
	if len(values) != len(want) {
		t.Fatalf("node.env = %v, want exactly %v", values, want)
	}
	for name, value := range want {
		if values[name] != value {
			t.Fatalf("%s = %q, want %q", name, values[name], value)
		}
	}
	got, err := loadTranscodeNodeEnv(func(name string) string { return values[name] })
	if err != nil || got.nodeKey != key || got.listen != ":8100" || got.coreURL != settings.server {
		t.Fatalf("node env = %+v, %v; want the installed key, URL and port", got, err)
	}

	// A rerun reads the same settings back, and keeps a variable an operator
	// added to node.env.
	added := map[string]string{"SILO_NODE_DRAIN_TIMEOUT": "15m"}
	stored, extra, err := storedNodeInstall(append(env, "SILO_NODE_DRAIN_TIMEOUT=15m\n"...), []byte(key+"\n"))
	if err != nil || stored != settings || !maps.Equal(extra, added) {
		t.Fatalf("stored = %+v, %v, %v; want %+v and %v", stored, extra, err, settings, added)
	}
	rerun, err := renderNodeEnv(stored, paths.key, extra)
	if err != nil {
		t.Fatal(err)
	}
	if values, err := godotenv.Unmarshal(string(rerun)); err != nil || values["SILO_NODE_DRAIN_TIMEOUT"] != "15m" || len(values) != len(want)+1 {
		t.Fatalf("rerun node.env = %v, %v; want the managed variables and the added one", values, err)
	}
}

func TestMergeNodeInstallFlags(t *testing.T) {
	stored := nodeInstallSettings{server: "http://localhost:8090", key: "stored-key", port: 8200}
	tests := []struct {
		name   string
		stored nodeInstallSettings
		args   []string
		want   nodeInstallSettings
	}{
		{"a rerun without flags keeps the install", stored, nil, stored},
		{"flags override", stored, []string{"--server", "https://silo.example", "--port", "9000"},
			nodeInstallSettings{server: "https://silo.example", key: "stored-key", port: 9000}},
		{"a first install gets the default port", nodeInstallSettings{}, []string{"--server", "http://localhost:8090", "--key", "k"},
			nodeInstallSettings{server: "http://localhost:8090", key: "k", port: defaultNodeInstallPort}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			given, help, err := parseNodeInstallFlags(tt.args)
			if err != nil || help {
				t.Fatalf("parse = %v, help %v", err, help)
			}
			if got := mergeNodeInstall(tt.stored, given); got != tt.want {
				t.Fatalf("merged = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// The plist's paths survive XML escaping.
func TestRenderNodePlistEscapesPaths(t *testing.T) {
	paths := nodeInstallPathsIn("/Users/a & <b>")
	var doc struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Value   string   `xml:",chardata"`
				Strings []string `xml:"string"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(renderNodePlist(paths), &doc); err != nil {
		t.Fatal(err)
	}
	values := map[string][]string{}
	items := doc.Dict.Items
	for i := 0; i+1 < len(items); i += 2 {
		value := items[i+1].Strings
		if items[i+1].XMLName.Local != "array" {
			value = []string{items[i+1].Value}
		}
		values[items[i].Value] = value
	}
	if got := values["ProgramArguments"]; !slices.Equal(got, []string{paths.binary, "-env", paths.env}) {
		t.Fatalf("ProgramArguments = %q", got)
	}
	if got := values["StandardErrorPath"]; !slices.Equal(got, []string{paths.log}) {
		t.Fatalf("StandardErrorPath = %q, want %q", got, paths.log)
	}
}
