package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

type recordingPeopleImageResolver struct {
	path    string
	variant string
}

type peopleImageSizeRepo struct {
	person models.Person
}

func (r peopleImageSizeRepo) Get(context.Context, int64) (*models.Person, error) {
	person := r.person
	return &person, nil
}

func (r peopleImageSizeRepo) Search(context.Context, string, int) ([]models.Person, error) {
	return []models.Person{r.person}, nil
}

func (peopleImageSizeRepo) Update(context.Context, models.Person) error {
	return nil
}

func (r *recordingPeopleImageResolver) ResolveImageURL(_ context.Context, path, variant string) string {
	r.path = path
	r.variant = variant
	return "resolved://" + path
}

func (r *recordingPeopleImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	resolved := make(map[string]string, len(paths))
	for _, path := range paths {
		resolved[path] = r.ResolveImageURL(ctx, path, variant)
	}
	return resolved
}

func TestPeopleResponseDefaultsProfilePhotoToMedium(t *testing.T) {
	const photoPath = "tmdb/people/287/profile/original.abc123.webp"

	tests := []struct {
		name        string
		size        imagesize.Size
		wantPath    string
		wantVariant string
	}{
		{name: "default", size: imagesize.Unset, wantPath: "tmdb/people/287/profile/w500.abc123.webp", wantVariant: imagesize.PluginVariantFeatured},
		{name: "explicit original", size: imagesize.Original, wantPath: photoPath, wantVariant: imagesize.PluginVariantOriginal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &recordingPeopleImageResolver{}
			detail := &catalog.DetailService{}
			detail.SetImageResolver(resolver)
			handler := &PeopleHandler{
				personRepo: peopleImageSizeRepo{person: models.Person{ID: 287, Name: "Person", PhotoPath: photoPath}},
				detailSvc:  detail,
			}
			path := "/api/v1/people"
			if tt.size != imagesize.Unset {
				path += "?image_size=" + string(tt.size)
			}
			recorder := httptest.NewRecorder()
			handler.HandleSearch(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var responses []personResponse
			if err := json.NewDecoder(recorder.Body).Decode(&responses); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(responses) != 1 {
				t.Fatalf("responses = %d, want 1", len(responses))
			}

			if resolver.path != tt.wantPath {
				t.Fatalf("resolved path = %q, want %q", resolver.path, tt.wantPath)
			}
			if resolver.variant != tt.wantVariant {
				t.Fatalf("resolver variant = %q, want %q", resolver.variant, tt.wantVariant)
			}
			if responses[0].PhotoURL == "" {
				t.Fatal("PhotoURL is empty")
			}
		})
	}
}
