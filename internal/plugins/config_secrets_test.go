package plugins

import (
	"reflect"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestGlobalConfigSecretPathsResolvesLocalRefsAndBranches(t *testing.T) {
	manifest := &pluginv1.PluginManifest{GlobalConfigSchema: []*pluginv1.ConfigSchema{{
		Key: "account",
		JsonSchema: `{
			"type":"object",
			"properties":{
				"connection":{"$ref":"#/$defs/connection"},
				"region":{"type":"string"}
			},
			"$defs":{
				"connection":{
					"type":"object",
					"properties":{
						"credentials":{
							"allOf":[{"$ref":"#/$defs/credentials"}]
						},
						"endpoint":{"type":"string"}
					}
				},
				"credentials":{
					"type":"object",
					"anyOf":[{
						"properties":{
							"api_key":{"type":"string","writeOnly":true}
						}
					}]
				}
			}
		}`,
	}}}

	got := GlobalConfigSecretPaths(manifest, "account")
	want := [][]string{{"connection", "credentials", "api_key"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("secret paths = %#v, want %#v", got, want)
	}

	publicFields, secretFields := GlobalConfigFieldSets(manifest, "account")
	if !reflect.DeepEqual(publicFields, []string{"region"}) ||
		!reflect.DeepEqual(secretFields, []string{"connection"}) {
		t.Fatalf("field sets = public %#v secret %#v", publicFields, secretFields)
	}
}

func TestGlobalConfigFieldSetsIncludesSecretsDeclaredByDependentSchemas(t *testing.T) {
	manifest := &pluginv1.PluginManifest{GlobalConfigSchema: []*pluginv1.ConfigSchema{{
		Key: "account",
		JsonSchema: `{
			"type":"object",
			"properties":{
				"mode":{"type":"string"}
			},
			"dependentSchemas":{
				"mode":{
					"properties":{
						"credentials":{"type":"string","format":"password"}
					}
				}
			}
		}`,
	}}}

	publicFields, secretFields := GlobalConfigFieldSets(manifest, "account")
	if !reflect.DeepEqual(publicFields, []string{"mode"}) ||
		!reflect.DeepEqual(secretFields, []string{"credentials"}) {
		t.Fatalf("field sets = public %#v secret %#v", publicFields, secretFields)
	}
	if got := GlobalConfigSecretPaths(manifest, "account"); !reflect.DeepEqual(
		got,
		[][]string{{"credentials"}},
	) {
		t.Fatalf("secret paths = %#v, want credentials", got)
	}
}
