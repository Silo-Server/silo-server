package apiv2

import (
	"encoding/json"
	"testing"
)

func TestReconcilePluginContent(t *testing.T) {
	inventory := func() []string {
		var out []string
		for _, m := range describePluginContent().Mounts {
			for _, method := range m.Methods {
				out = append(out, method+" "+m.Path)
			}
		}
		return out
	}
	for _, name := range []string{"exact", "missing extension", "missing mount", "missing method", "unserved", "unexpected served path", "unexpected served method", "unexpected documented path", "unexpected documented method", "prefix", "duplicate mount", "duplicate method", "finite collision"} {
		t.Run(name, func(t *testing.T) {
			description := describePluginContent()
			// Method slices for the two proxy mounts share backing storage by design.
			for i := range description.Mounts {
				description.Mounts[i].Methods = append([]string(nil), description.Mounts[i].Methods...)
			}
			doc := map[string]any{pluginContentExtension: description, "paths": map[string]any{}}
			observed := inventory()
			wantErr, wantUnaccounted, wantUnserved := false, 0, 0
			switch name {
			case "missing extension":
				delete(doc, pluginContentExtension)
				wantUnaccounted = 20
			case "missing mount":
				description.Mounts = description.Mounts[1:]
				wantErr = true
			case "missing method":
				description.Mounts[0].Methods = description.Mounts[0].Methods[1:]
				wantErr = true
			case "unserved":
				observed = observed[1:]
				wantUnserved = 1
			case "unexpected served path":
				observed = append(observed, "GET /api/v2/plugin-content/unlisted/*")
				wantUnaccounted = 1
			case "unexpected served method":
				observed = append(observed, "POST "+description.Mounts[2].Path)
				wantUnaccounted = 1
			case "unexpected documented path":
				description.Mounts[0].Path = "/api/v2/unrelated/*"
				wantErr = true
			case "unexpected documented method":
				description.Mounts[2].Methods = append(description.Mounts[2].Methods, "POST")
				wantErr = true
			case "prefix":
				description.Prefix = "/api/v2"
				wantErr = true
			case "duplicate mount":
				description.Mounts = append(description.Mounts, description.Mounts[0])
				wantErr = true
			case "duplicate method":
				description.Mounts[0].Methods = append(description.Mounts[0].Methods, "GET")
				wantErr = true
			case "finite collision":
				doc["paths"] = map[string]any{description.Mounts[0].Path: map[string]any{"get": map[string]any{}}}
				wantErr = true
			}
			if name != "missing extension" {
				doc[pluginContentExtension] = description
			}
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			unaccounted, unserved, err := reconcileSpec(observed, data)
			if (err != nil) != wantErr || len(unaccounted) != wantUnaccounted || len(unserved) != wantUnserved {
				t.Fatalf("unaccounted=%v unserved=%v err=%v", unaccounted, unserved, err)
			}
		})
	}
}
