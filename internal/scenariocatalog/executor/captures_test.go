package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestResponseCaptureValuesAreNotTemplates(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get(capturedCursorQuery) != "${member_token}&scope=household" || r.Header.Get("If-Match") != `"revision"` {
			t.Errorf("capture changed: %s %v", r.URL, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	e := &Env{}
	prev := response{Headers: http.Header{"Etag": {`"revision"`}}, IsJSON: true, Doc: map[string]any{"page": map[string]any{capturedCursorQuery: "${member_token}&scope=household"}}}
	bindings := []scenariocatalog.ResponseBinding{{Header: "ETag", RequestHeader: "If-Match"}, {Pointer: new("/page/cursor"), Query: capturedCursorQuery}}
	_, failures, err := e.exchange(server.URL, "GET", scenariocatalog.Request{Path: "/api/v2/devices", Repeat: 2}, scenariocatalog.Principal{Class: "public"}, scenariocatalog.Expect{Status: 200}, &prev, bindings, map[string]bool{})
	if err != nil || len(failures) > 0 || calls != 2 {
		t.Fatalf("exchange: %v %v calls=%d", err, failures, calls)
	}
}

func TestResponseCaptureFailureStopsDependentRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = fmt.Fprint(w, `{}`) }))
	defer server.Close()
	e := &Env{}
	binding := []scenariocatalog.ResponseBinding{{Pointer: new("/cursor"), Query: capturedCursorQuery}}
	for _, value := range []any{nil, 42, true, []any{"x"}, map[string]any{"x": "y"}, "", strings.Repeat("x", scenariocatalog.MaxCapturedStringBytes+1)} {
		previous := response{IsJSON: true, Doc: map[string]any{capturedCursorQuery: value}}
		if _, _, err := e.exchange(server.URL, "GET", scenariocatalog.Request{Path: "/api/v2/devices"}, scenariocatalog.Principal{Class: "public"}, scenariocatalog.Expect{Status: 200}, &previous, binding, nil); err == nil {
			t.Fatal("invalid capture accepted")
		}
	}
	for _, previous := range []*response{nil, {IsJSON: true, Doc: map[string]any{}}} {
		if _, _, err := e.exchange(server.URL, "GET", scenariocatalog.Request{Path: "/api/v2/devices"}, scenariocatalog.Principal{Class: "public"}, scenariocatalog.Expect{Status: 200}, previous, binding, nil); err == nil {
			t.Fatal("missing previous capture accepted")
		}
	}
	seen := map[string]bool{"already-read": true}
	previous := response{IsJSON: true, Doc: map[string]any{capturedCursorQuery: "already-read"}}
	if _, _, err := e.exchange(server.URL, "GET", scenariocatalog.Request{Path: "/api/v2/devices"}, scenariocatalog.Principal{Class: "public"}, scenariocatalog.Expect{Status: 200}, &previous, binding, seen); err == nil {
		t.Fatal("repeated cursor accepted")
	}
	for _, values := range [][]string{nil, {"one", "two"}, {"bad\r\nvalue"}} {
		previous := response{Headers: http.Header{"Etag": values}}
		if _, _, err := e.exchange(server.URL, "GET", scenariocatalog.Request{Path: "/api/v2/devices"}, scenariocatalog.Principal{Class: "public"}, scenariocatalog.Expect{Status: 200}, &previous, []scenariocatalog.ResponseBinding{{Header: "ETag", RequestHeader: "If-Match"}}, nil); err == nil {
			t.Fatal("invalid header capture accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("sent %d dependent requests after capture failures", calls)
	}
}

func TestV2FollowupsDoNotInheritLegacyState(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/devices" {
			if len(paths) == 3 && r.Header.Get("If-Match") != `"v2"` {
				t.Errorf("wrong capture %q", r.Header.Get("If-Match"))
			}
			w.Header().Set("ETag", `"v2"`)
		} else {
			w.Header().Set("ETag", `"v1"`)
		}
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	var row scenariocatalog.Row
	for _, c := range catalogs {
		for _, r := range c.Rows {
			if r.Path == "/api/v1/devices/" && r.Method == "GET" {
				row = r
			}
		}
	}
	s := row.Scenarios[0]
	s.Principal = scenariocatalog.Principal{Class: "public"}
	s.Requires = []string{"database_unavailable"}
	s.Request = scenariocatalog.Request{Path: "/legacy"}
	s.Expect = scenariocatalog.Expect{Status: 200}
	s.Then = []scenariocatalog.Step{{Method: "GET", Request: scenariocatalog.Request{Path: "/legacy-followup"}, Expect: scenariocatalog.Expect{Status: 200}}}
	s.V2Expectation = &scenariocatalog.V2Expectation{OperationID: "listDevices", Method: "GET", Request: scenariocatalog.Request{Path: "/api/v2/devices"}, Expect: scenariocatalog.Expect{Status: 200}, Then: []scenariocatalog.V2Step{{OperationID: "listDevices", Step: scenariocatalog.Step{Method: "GET", Request: scenariocatalog.Request{Path: "/api/v2/devices"}, Expect: scenariocatalog.Expect{Status: 200}}, FromPrevious: []scenariocatalog.ResponseBinding{{Header: "ETag", RequestHeader: "If-Match"}}}}}
	if len(v2Scenario(s).Then) != 0 || len(s.Then) != 1 {
		t.Fatal("v2 inherited or changed legacy follow-ups")
	}
	// Legacy follow-ups deliberately require a database. The offline exchanges
	// below exercise response isolation without weakening that guard.
	s.Then = nil
	e := &Env{offline: server}
	var results []Result
	e.Run(t, &scenariocatalog.Catalog{File: "synthetic"}, row, s, func(r Result) { results = append(results, r) })
	if strings.Join(paths, ",") != "/legacy,/api/v2/devices,/api/v2/devices" {
		t.Fatalf("transport leakage: %v", paths)
	}
	for _, r := range results {
		if !r.Passed() {
			t.Fatalf("exchange: %+v", r)
		}
	}
}

func TestResponseCaptureBoundaryAndEscapedPointer(t *testing.T) {
	previous := response{IsJSON: true, Doc: map[string]any{"a/b": map[string]any{"~key": []any{strings.Repeat("x", scenariocatalog.MaxCapturedStringBytes)}}}}
	req := httptest.NewRequest("GET", "http://example.test/api/v2/devices", nil)
	if err := applyResponseBindings(req, &previous, []scenariocatalog.ResponseBinding{{Pointer: new("/a~1b/~0key/0"), Query: capturedCursorQuery}}, map[string]bool{}, true); err != nil {
		t.Fatal(err)
	}
	if len(req.URL.Query().Get(capturedCursorQuery)) != scenariocatalog.MaxCapturedStringBytes {
		t.Fatal("boundary capture changed")
	}
}
