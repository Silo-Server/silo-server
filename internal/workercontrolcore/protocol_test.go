package workercontrolcore

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestProtocolOutputDTOsRetainRuntimeJSONKeys(t *testing.T) {
	for _, body := range []any{outputProtocolRequest{Token: "opaque", NodeURL: "https://worker.example"}} {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]string
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["token"] != "opaque" || fields["Token"] != "" {
			t.Fatalf("token wire keys drifted: %s", raw)
		}
		if len(fields) == 2 && fields["node_url"] != "https://worker.example" {
			t.Fatalf("node wire key drifted: %s", raw)
		}
	}
}

func TestProtocolWorkerSchemasOmitCoreCredentialIdentity(t *testing.T) {
	schemas := huma.NewMapRegistry("#/schemas/", func(t reflect.Type, hint string) string {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		name := huma.DefaultSchemaNamer(t, hint)
		if pkg := t.PkgPath(); pkg != "" {
			parts := strings.Split(pkg, "/")
			return parts[len(parts)-1] + "_" + name
		}
		return name
	})
	ProtocolControls(schemas)
	encoded, err := json.Marshal(schemas.Map())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"olsid":`, `"oakid":`} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Errorf("worker schema advertises core-only credential property %s", key)
		}
	}
	// Schema visibility must not discard the credential from core-owned durable
	// claims and recipes: reconstruction needs the same originating credential.
	for _, origin := range []streamtoken.OriginCredential{{OriginLoginSessionID: "original-login"}, {OriginAPIKeyID: 12}} {
		for _, value := range []any{streamtoken.Claims{OriginCredential: origin}, playback.RecipeCard{OriginCredential: origin}} {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var restored streamtoken.OriginCredential
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			if restored != origin {
				t.Fatalf("core persistence discarded source credential for %T: %+v", value, restored)
			}
		}
	}
}
