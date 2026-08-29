package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// The API validates a frozen remux recipe against the proxy that will execute
// it, so a proxy must answer the same capability probe a transcode node does.
// Without it the API cannot tell a capable proxy from one on an older ffmpeg,
// and the mismatch only surfaces as a failed stream.
func TestProxyAdvertisesTransformationCapabilities(t *testing.T) {
	const secret = "capability-secret"
	server := newCapabilityProxyServer(t, secret)

	request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var info playback.HWAccelInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	// The probe reflects whatever ffmpeg this machine has; the contract under
	// test is that the endpoint exists and answers in the shared shape.
	for _, transformation := range info.Transformations {
		if transformation.Name == "" || transformation.RecipeVersion == "" {
			t.Fatalf("advertised transformation is missing identity: %#v", transformation)
		}
	}
}

func TestProxyCapabilitiesRequireBearer(t *testing.T) {
	server := newCapabilityProxyServer(t, "capability-secret")

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unauthenticated capability probe", recorder.Code)
	}
}
