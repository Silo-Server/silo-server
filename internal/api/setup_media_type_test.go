package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestInitialSetupRequiresJSONDB(t *testing.T) {
	for _, browser := range []bool{false, true} {
		name := "native"
		if browser {
			name = "browser"
		}
		t.Run(name, func(t *testing.T) {
			d := atomicSetupDatabase(t, "media_type_"+name)
			server := d.router(t, pgstore.NewPostgresProvider(d.pool))
			body := `{"username":"owner","email":"owner@example.test","password":"` + atomicSetupPassword + `"}`
			for _, mediaType := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=fixture", ""} {
				r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/auth/setup", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Content-Type", mediaType)
				r.Header.Set("Origin", "https://other.example.test")
				r.Header.Set("Sec-Fetch-Site", "cross-site")
				resp, err := server.Client().Do(r)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusUnsupportedMediaType {
					t.Fatalf("media type %q: status = %d, want 415", mediaType, resp.StatusCode)
				}
				if got := d.counts(t); got != (atomicCounts{}) {
					t.Fatalf("rejected setup created rows: %+v", got)
				}
			}

			r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/auth/setup", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
			if browser {
				r.Header.Set("Origin", server.URL)
				r.Header.Set("Sec-Fetch-Site", "same-origin")
			}
			resp, err := server.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("legitimate %s setup: status = %d, want 201", name, resp.StatusCode)
			}
			if got := d.counts(t); got != (atomicCounts{users: 1, admins: 1, sessions: 1}) {
				t.Fatalf("legitimate setup rows: %+v", got)
			}
		})
	}
}
