package abs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSkipMediaCompression(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/items/i1/file/2", true},
		{http.MethodGet, "/api/items/i1/file/2/download", true},
		{http.MethodHead, "/abs/api/items/i1/file/2", true},
		{http.MethodGet, "/abs/api/items/i1/file/2/download", true},
		{http.MethodGet, "/public/session/s1/track/2", true},
		{http.MethodGet, "/abs/public/session/s1/track/2", true},
		{http.MethodGet, "/feed/books/file/2", true},
		{http.MethodGet, "/socket.io/", false},
		{http.MethodGet, "/api/items/i1/file/2/extra", false},
		{http.MethodPut, "/api/items/i1/file/2", false},
		{http.MethodGet, "/API/items/i1/file/2", false},
		{http.MethodGet, "/api/items/i1/file/2/", false},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if got := SkipMediaCompression(httptest.NewRequest(tt.method, tt.path, nil)); got != tt.want {
				t.Fatalf("SkipMediaCompression = %v, want %v", got, tt.want)
			}
		})
	}
}
