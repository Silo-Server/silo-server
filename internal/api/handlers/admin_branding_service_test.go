package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/branding"
)

type memBrandingSettings struct{ values map[string]string }

func (m *memBrandingSettings) Get(_ context.Context, key string) (string, error) {
	return m.values[key], nil
}
func (m *memBrandingSettings) Set(_ context.Context, key, value string) error {
	m.values[key] = value
	return nil
}

type memBrandingStore struct{ objects map[string][]byte }

func (m *memBrandingStore) Put(_ context.Context, key string, data []byte) error {
	m.objects[key] = data
	return nil
}
func (m *memBrandingStore) Get(_ context.Context, key string) (io.ReadCloser, artworkstore.ObjectInfo, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, artworkstore.ObjectInfo{}, artworkstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), artworkstore.ObjectInfo{Key: key, Size: int64(len(data))}, nil
}
func (m *memBrandingStore) Stat(_ context.Context, key string) (artworkstore.ObjectInfo, error) {
	data, ok := m.objects[key]
	if !ok {
		return artworkstore.ObjectInfo{}, artworkstore.ErrNotFound
	}
	return artworkstore.ObjectInfo{Key: key, Size: int64(len(data))}, nil
}

// The v2 seam keeps the v1 decisions: kind and image-type failures are field
// validation errors, missing storage is unavailability, and a favicon upload
// stores content-addressed bytes then points the slot at them; delete clears
// the slot and leaves the object.
func TestAdminBrandingAssetSeam(t *testing.T) {
	settings := &memBrandingSettings{values: map[string]string{}}
	store := &memBrandingStore{objects: map[string][]byte{}}
	h := NewBrandingHandler(branding.NewService(settings, store))
	ctx := t.Context()
	field := func(t *testing.T, err error, want string) {
		t.Helper()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Field != want {
			t.Fatalf("err = %v, want 400 at %s", err, want)
		}
	}
	_, err := h.UploadAdminBrandingAsset(ctx, "banner", "image/png", []byte("x"))
	field(t, err, "kind")
	_, err = h.UploadAdminBrandingAsset(ctx, "favicon", "image/gif", []byte("x"))
	field(t, err, "file")
	view, err := h.UploadAdminBrandingAsset(ctx, "favicon", "image/x-icon", []byte("icon-bytes"))
	if err != nil || view.Kind != "favicon" || view.Ref == "" || view.URL != branding.AssetURLFor(branding.KindFavicon, view.Ref) {
		t.Fatalf("upload: %+v %v", view, err)
	}
	if settings.values["branding.favicon_ref"] != view.Ref || string(store.objects["branding/favicon/"+view.Ref]) != "icon-bytes" {
		t.Fatalf("stored %v %v", settings.values, store.objects)
	}
	if err := h.DeleteAdminBrandingAsset(ctx, "banner"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := h.DeleteAdminBrandingAsset(ctx, "favicon"); err != nil || settings.values["branding.favicon_ref"] != "" {
		t.Fatalf("delete: %v %v", err, settings.values)
	}
	if _, ok := store.objects["branding/favicon/"+view.Ref]; !ok {
		t.Fatal("delete removed the stored object")
	}
	noStore := NewBrandingHandler(branding.NewService(settings, nil))
	var apiErr *APIError
	if _, err := noStore.UploadAdminBrandingAsset(ctx, "favicon", "image/png", []byte("x")); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("no storage: %v", err)
	}
	if err := (*BrandingHandler)(nil).DeleteAdminBrandingAsset(ctx, "favicon"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("nil handler: %v", err)
	}
}
