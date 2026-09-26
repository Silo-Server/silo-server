package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type contentFixture struct{ calls int }

func (*contentFixture) ContentAvailable() bool { return true }
func (f *contentFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(299)
	_, _ = w.Write([]byte("{opaque plugin bytes"))
}

func TestPluginContentDynamicRegistration(t *testing.T) {
	service := &contentFixture{}
	handler := NewHandler(Dependencies{PluginContent: service, testRegister: func(reg *Registry) {
		for _, d := range reg.Declared() {
			if strings.Contains(d.Path, "plugin-content/plugins") {
				t.Fatal("dynamic route entered finite registry")
			}
		}
	}})
	for _, mount := range describePluginContent().Mounts {
		path := strings.ReplaceAll(strings.ReplaceAll(mount.Path, "{installation_id}", "1"), "*", "nested/page")
		for _, method := range mount.Methods {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(method, path+"?opaque=yes", nil))
			if rec.Code != 299 || rec.Body.String() != "{opaque plugin bytes" || rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
			}
		}
	}
	for _, path := range []string{"/plugin-assets/1", "/plugin-assets/1/nested/file"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, plugins.ContentPrefix+path, nil))
		if rec.Code != 405 || rec.Header().Get("Allow") != "GET" {
			t.Fatal(rec.Code, rec.Header())
		}
	}
	data, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths   map[string]json.RawMessage
		Content pluginContentDescription `json:"x-silo-plugin-content"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Content.Mounts) != 4 || doc.Content.Prefix != plugins.ContentPrefix {
		t.Fatal("missing dynamic description")
	}
	for _, m := range doc.Content.Mounts {
		if _, exists := doc.Paths[m.Path]; exists {
			t.Fatal("fabricated finite path", m.Path)
		}
	}
	if _, exists := doc.Paths[plugins.ContentPrefix+"/capabilities"]; !exists {
		t.Fatal("missing capability")
	}
}

func TestPluginContentUnavailable(t *testing.T) {
	handler := NewHandler(Dependencies{})
	for _, mount := range describePluginContent().Mounts {
		path := strings.ReplaceAll(strings.ReplaceAll(mount.Path, "{installation_id}", "1"), "*", "file")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(mount.Methods[0], path, nil))
		if rec.Code != 503 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Fatal(rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, plugins.ContentPrefix+"/capabilities", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "not_configured") {
		t.Fatal(rec.Code, rec.Body)
	}
}
