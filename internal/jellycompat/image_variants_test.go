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
		// The large bucket is a width rung, so a height-only constraint must not
		// reach it: a portrait poster at MaxHeight=900 would come back far taller
		// than the client asked for.
		{"height only does not reach large", "/Items/1/Images/Primary?MaxHeight=900", "Primary", "medium"},
		{"width reaches large", "/Items/1/Images/Primary?MaxWidth=900", "Primary", "large"},
		{"fill width reaches large", "/Items/1/Images/Primary?FillWidth=900", "Primary", "large"},
		{"fill height only does not reach large", "/Items/1/Images/Primary?FillHeight=900", "Primary", "medium"},
		{"fill height only backdrop stays medium", "/Items/1/Images/Backdrop?FillHeight=1100", "Backdrop", "medium"},
		{"large from fill width", "/Items/1/Images/Backdrop?FillWidth=1100", "Backdrop", "large"},
		// The pre-existing original bucket keeps reading any dimension; it serves
		// the stored original rather than a width rung.
		{"height only still reaches original", "/Items/1/Images/Primary?MaxHeight=1200", "Primary", "original"},
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
