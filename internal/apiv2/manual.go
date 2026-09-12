package apiv2

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// reconcileSpec compares the routes a router serves with the operations a
// committed OpenAPI document describes, its closed plugin-content mount inventory,
// and returns the served routes no document or extension accounts for, and the
// documented or registered routes the router does not serve. Both must be
// empty: a structured route cannot bypass Huma, and the artifact cannot
// describe a route the binary does not serve.
func reconcileSpec(observed []string, doc []byte) (unaccounted, unserved []string, err error) {
	var parsed struct {
		Paths         map[string]map[string]json.RawMessage `json:"paths"`
		PluginContent *pluginContentDescription             `json:"x-silo-plugin-content"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, nil, err
	}
	expected := map[string]string{}
	for path, item := range parsed.Paths {
		for method := range item {
			expected[strings.ToUpper(method)+" "+path] = "openapi.json"
		}
	}
	if parsed.PluginContent != nil {
		canonical := describePluginContent()
		if parsed.PluginContent.Prefix != canonical.Prefix {
			return nil, nil, fmt.Errorf("unexpected %s prefix", pluginContentExtension)
		}
		allowed := map[string]bool{}
		for _, mount := range canonical.Mounts {
			for _, method := range mount.Methods {
				allowed[method+" "+mount.Path] = true
			}
		}
		seenPaths := map[string]bool{}
		for _, mount := range parsed.PluginContent.Mounts {
			if seenPaths[mount.Path] || len(mount.Methods) == 0 {
				return nil, nil, fmt.Errorf("duplicate or empty %s mount: %s", pluginContentExtension, mount.Path)
			}
			seenPaths[mount.Path] = true
			for _, method := range mount.Methods {
				key := method + " " + mount.Path
				if prev, dup := expected[key]; dup {
					return nil, nil, fmt.Errorf("%s is both a %s entry and a %s entry", key, prev, pluginContentExtension)
				}
				if !allowed[key] {
					return nil, nil, fmt.Errorf("unexpected %s route: %s", pluginContentExtension, key)
				}
				delete(allowed, key)
				expected[key] = pluginContentExtension
			}
		}
		if len(allowed) != 0 {
			return nil, nil, fmt.Errorf("%s is missing %d required routes", pluginContentExtension, len(allowed))
		}
	}
	served := map[string]bool{}
	for _, o := range observed {
		served[o] = true
		if _, ok := expected[o]; !ok {
			unaccounted = append(unaccounted, o)
		}
	}
	for key := range expected {
		if !served[key] {
			unserved = append(unserved, key+" ("+expected[key]+")")
		}
	}
	sort.Strings(unaccounted)
	sort.Strings(unserved)
	return unaccounted, unserved, nil
}
