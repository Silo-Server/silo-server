package requests

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Each way a probe fails gets a host-written answer: a field error for what
// the admin typed wrong, or an unreachable error whose detail says why. The
// plugin's transport text never becomes the answer; only a message the
// plugin wrote itself as InvalidArgument or FailedPrecondition is shown.
func TestLoadIntegrationOptionsClassifiesProbeFailures(t *testing.T) {
	grpcUnknown := func(msg string) error { return status.Error(codes.Unknown, msg) }
	cases := []struct {
		name       string
		capability string
		err        error
		field      string
		fieldText  string
		formError  string
		detail     string
	}{
		{name: "unauthorized", err: grpcUnknown("httpclient: HTTP 401: Unauthorized"), field: "api_key_ref", fieldText: integrationKeyRejected},
		{name: "forbidden", err: errors.New("httpclient: HTTP 403"), field: "api_key_ref", fieldText: integrationKeyRejected},
		{name: "not found arr", capability: "arr", err: grpcUnknown("httpclient: HTTP 404: <html>not found</html>"), field: "base_url", fieldText: integrationNotArr},
		{name: "web page instead of the api", capability: "arr", err: grpcUnknown("httpclient: decode response: invalid character '<' looking for beginning of value"), field: "base_url", fieldText: integrationNotArr},
		{name: "truncated json", capability: "arr", err: grpcUnknown("httpclient: decode response: unexpected EOF"), detail: ""},
		{name: "not found other plugin", capability: "seerr", err: grpcUnknown("httpclient: HTTP 404"), field: "base_url", fieldText: integrationNotService},
		{name: "http on https", err: grpcUnknown(`httpclient: request failed: Get "https://10.0.0.5:8989/api/v3/rootfolder": http: server gave HTTP response to HTTPS client`), field: "base_url", fieldText: integrationHTTPNotHTTPS},
		{name: "no scheme", err: grpcUnknown(`httpclient: request failed: Get "10.0.0.5:8989/api/v3/rootfolder": unsupported protocol scheme ""`), field: "base_url", fieldText: integrationAddressMessage},
		{name: "plugin missing key", err: grpcUnknown("httpclient: api key is required"), field: "api_key_ref", fieldText: integrationKeyMissing},
		{name: "refused", err: grpcUnknown(`httpclient: request failed: Get "http://10.0.0.5:8990/api/v3/rootfolder": dial tcp 10.0.0.5:8990: connect: connection refused`), detail: integrationRefused},
		{name: "no such host", err: grpcUnknown("dial tcp: lookup sonarr.invalid: no such host"), detail: integrationNoSuchHost},
		{name: "client timeout", err: grpcUnknown("net/http: request canceled (Client.Timeout exceeded while awaiting headers)"), detail: integrationTimedOut},
		{name: "grpc deadline", err: status.Error(codes.DeadlineExceeded, "context deadline exceeded"), detail: integrationTimedOut},
		{name: "certificate", err: grpcUnknown("tls: failed to verify certificate: x509: certificate signed by unknown authority"), detail: integrationBadCertificate},
		{name: "unknown", err: grpcUnknown("httpclient: HTTP 500: kaboom"), detail: ""},
		{name: "plugin invalid argument", err: status.Error(codes.InvalidArgument, "This is Lidarr, not Sonarr or Radarr."), formError: "This is Lidarr, not Sonarr or Radarr."},
		{name: "wrapped plugin failed precondition", err: fmt.Errorf("router: %w", status.Error(codes.FailedPrecondition, "Set up the server first.")), formError: "Set up the server first."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &fakeRouterProvider{optionsErr: tc.err}
			service := newTestService(newFakeStore())
			service.SetRouterProvider(router)
			install := 1
			capability := tc.capability
			if capability == "" {
				capability = "arr"
			}
			_, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
				ID: "new", CapabilityID: capability, InstallationID: &install, BaseURL: "http://10.0.0.5:8989", APIKeyRef: "key",
			})
			if tc.field != "" || tc.formError != "" {
				var ve *ValidationError
				var probe *ProbeValidationError
				if !errors.As(err, &ve) || !errors.As(err, &probe) {
					t.Fatalf("err = %v, want a classified *ValidationError", err)
				}
				if tc.field != "" && ve.FieldErrors[tc.field] != tc.fieldText {
					t.Fatalf("field errors = %+v, want %s = %q", ve.FieldErrors, tc.field, tc.fieldText)
				}
				if ve.FormError != tc.formError {
					t.Fatalf("form error = %q, want %q", ve.FormError, tc.formError)
				}
				return
			}
			var unreachable *IntegrationUnreachableError
			if !errors.As(err, &unreachable) || !errors.Is(err, ErrIntegrationUnreachable) {
				t.Fatalf("err = %v, want *IntegrationUnreachableError", err)
			}
			if unreachable.Detail != tc.detail {
				t.Fatalf("detail = %q, want %q", unreachable.Detail, tc.detail)
			}
		})
	}
}

// The probe refuses a missing key itself, and passes the address on as given:
// normalizing it is the v2 adapter's job, and the frozen v1 route sends what
// its client submitted.
func TestLoadIntegrationOptionsChecksKeyAndPassesAddressThrough(t *testing.T) {
	install := 1
	probe := func(baseURL, key string) (*fakeRouterProvider, error) {
		router := &fakeRouterProvider{}
		service := newTestService(newFakeStore())
		service.SetRouterProvider(router)
		_, err := service.LoadIntegrationOptions(context.Background(), Viewer{UserID: 1, IsAdmin: true}, Integration{
			ID: "new", CapabilityID: "arr", InstallationID: &install, BaseURL: baseURL, APIKeyRef: key,
		})
		return router, err
	}

	router, err := probe("10.0.0.5:8989", "key")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if router.gotOptionsConn.BaseURL != "10.0.0.5:8989" {
		t.Fatalf("probe base URL = %q, want it as given", router.gotOptionsConn.BaseURL)
	}

	router, err = probe("http://10.0.0.5:8989", "")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.FieldErrors["api_key_ref"] != integrationKeyMissing {
		t.Fatalf("err = %v, want api_key_ref field error", err)
	}
	if router.gotOptionsConn.BaseURL != "" {
		t.Fatal("plugin was asked without a key")
	}
}

func TestNormalizeIntegrationBaseURL(t *testing.T) {
	for _, bad := range []string{"", "ftp://10.0.0.5", "http://", "http://user:pw@10.0.0.5:8989", "http://10.0.0.5:8989/?a=1", "http://10.0.0.5:8989/#x"} {
		if got, err := normalizeIntegrationBaseURL(bad); err == nil {
			t.Errorf("normalizeIntegrationBaseURL(%q) = %q, want an error", bad, got)
		}
	}
	for in, want := range map[string]string{
		"10.0.0.5:8989":              "http://10.0.0.5:8989",
		"sonarr.lan":                 "http://sonarr.lan",
		"https://sonarr.lan/sonarr/": "https://sonarr.lan/sonarr",
		"HTTP://sonarr.lan:8989":     "http://sonarr.lan:8989",
		"  http://[::1]:8989  ":      "http://[::1]:8989",
	} {
		got, err := normalizeIntegrationBaseURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeIntegrationBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// The service saves the address as given: the v2 adapter normalizes it with
// NormalizeIntegrationBaseURL, and the frozen v1 save path stays unchanged.
// A normalized form of the saved address still counts as unchanged for
// keeping the stored key.
func TestSaveIntegrationLeavesBaseURLToTheCaller(t *testing.T) {
	store := newFakeStore()
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)
	admin := Viewer{UserID: 1, IsAdmin: true}
	install := 1

	created, err := service.CreateIntegration(context.Background(), admin, Integration{
		Name: "Sonarr", CapabilityID: "arr", InstallationID: &install, BaseURL: "10.0.0.5:8989/", APIKeyRef: "key",
	})
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	if created.BaseURL != "10.0.0.5:8989/" {
		t.Fatalf("created base_url = %q, want it as given", created.BaseURL)
	}

	stored := routerInst("router-1")
	stored.BaseURL = "http://router-1.local/"
	store.integrations = append(store.integrations, stored)
	if _, err := service.UpdateIntegration(context.Background(), admin, Integration{
		ID: "router-1", Name: "router-1", CapabilityID: "arr", InstallationID: &install, BaseURL: "http://router-1.local",
	}); err != nil {
		t.Fatalf("UpdateIntegration with the normalized saved address: %v", err)
	}
	if router.validateCalls != 2 {
		t.Fatalf("plugin Validate calls = %d, want 2", router.validateCalls)
	}
}

func TestNormalizeIntegrationBaseURLRefusesAsFieldError(t *testing.T) {
	if got, err := NormalizeIntegrationBaseURL("10.0.0.5:8989/"); err != nil || got != "http://10.0.0.5:8989" {
		t.Fatalf("NormalizeIntegrationBaseURL = %q, %v", got, err)
	}
	_, err := NormalizeIntegrationBaseURL("ftp://10.0.0.5")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.FieldErrors["base_url"] != integrationAddressMessage {
		t.Fatalf("err = %v, want base_url field error", err)
	}
}
