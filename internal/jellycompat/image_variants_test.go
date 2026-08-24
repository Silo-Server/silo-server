package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompatRequestImageSize(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		imageType string
		want      string
	}{
		{"no request defaults to card", "", "Primary", "small"},
		{"no request backdrop defaults to medium", "", "Backdrop", "medium"},
		{"no dimensions", "/Items/1/Images/Primary", "Primary", "small"},
		{"no dimensions backdrop", "/Items/1/Images/Backdrop", "Backdrop", "medium"},
		{"thumbnail width", "/Items/1/Images/Primary?MaxWidth=200", "Primary", "small"},
		{"card boundary", "/Items/1/Images/Primary?FillWidth=320", "Primary", "small"},
		{"between card and large", "/Items/1/Images/Primary?MaxWidth=600", "Primary", "medium"},
		{"just below large", "/Items/1/Images/Primary?MaxWidth=779", "Primary", "medium"},
		{"large boundary", "/Items/1/Images/Primary?MaxWidth=780", "Primary", "large"},
		{"large from height", "/Items/1/Images/Primary?MaxHeight=900", "Primary", "large"},
		{"large from fill", "/Items/1/Images/Backdrop?FillHeight=1100", "Backdrop", "large"},
		{"just below original", "/Items/1/Images/Backdrop?MaxWidth=1199", "Backdrop", "large"},
		{"original boundary", "/Items/1/Images/Backdrop?MaxWidth=1200", "Backdrop", "original"},
		{"very large", "/Items/1/Images/Backdrop?MaxWidth=4000", "Backdrop", "original"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.query != "" {
				req = httptest.NewRequest(http.MethodGet, tt.query, nil)
			}
			if got := compatRequestImageSize(req, tt.imageType); got != tt.want {
				t.Fatalf("compatRequestImageSize(%q, %q) = %q, want %q", tt.query, tt.imageType, got, tt.want)
			}
		})
	}
}
