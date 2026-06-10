package audiobooks

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type fakeSettingsReader struct {
	value string
	err   error
}

func (f *fakeSettingsReader) GetString(_ context.Context, key string) (string, error) {
	if key != "audiobookshelf_compat.enabled" {
		return "", errors.New("unexpected key: " + key)
	}
	return f.value, f.err
}

func TestServiceABSCompatEnabledReadsFlag(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		want   bool
	}{
		{"flag true", "true", true},
		{"flag false", "false", false},
		{"flag empty defaults false", "", false},
		{"flag garbage defaults false", "yes-please", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			svc := New(&fakeSettingsReader{value: tc.stored})
			got, err := svc.ABSCompatEnabled(context.Background())
			if err != nil {
				t.Fatalf("ABSCompatEnabled returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ABSCompatEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServiceABSCompatEnabledPropagatesError(t *testing.T) {
	wantErr := errors.New("db down")
	svc := New(&fakeSettingsReader{err: wantErr})
	_, err := svc.ABSCompatEnabled(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ABSCompatEnabled error = %v, want %v wrapped", err, wantErr)
	}
}

func TestServiceABSCompatEnabledNilReceiverReturnsFalse(t *testing.T) {
	var svc *Service
	got, err := svc.ABSCompatEnabled(context.Background())
	if err != nil {
		t.Fatalf("ABSCompatEnabled returned error: %v", err)
	}
	if got {
		t.Fatal("ABSCompatEnabled = true, want false")
	}
}

func TestServiceABSCompatEnabledNilSettingsReturnsFalse(t *testing.T) {
	svc := New(nil)
	got, err := svc.ABSCompatEnabled(context.Background())
	if err != nil {
		t.Fatalf("ABSCompatEnabled returned error: %v", err)
	}
	if got {
		t.Fatal("ABSCompatEnabled = true, want false")
	}
}

func TestBuildABSHandlerCoverResolverUsesPosterVariant(t *testing.T) {
	resolver := &recordingImageResolver{}
	detail := &catalog.DetailService{}
	detail.SetImageResolver(resolver)

	handler := New(nil).BuildABSHandler(ABSHandlerDeps{
		Items:  &catalog.ItemRepository{},
		Files:  &scanner.FileRepository{},
		Detail: detail,
	})

	coverResolver := absCoverResolverForTest(t, handler)
	got := coverResolver(context.Background(), "local/audiobooks/book-1/poster/original.webp", "card")

	if !strings.Contains(got, "/w500.webp") {
		t.Fatalf("resolved URL = %q, want w500 poster variant", got)
	}
	if resolver.variant != "featured" {
		t.Fatalf("resolver variant = %q, want featured", resolver.variant)
	}
}

func absCoverResolverForTest(t *testing.T, handler *abs.Handler) func(context.Context, string, string) string {
	t.Helper()
	field := reflect.ValueOf(handler).Elem().FieldByName("deps").FieldByName("CoverResolver")
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface().(func(context.Context, string, string) string)
}

type recordingImageResolver struct {
	path    string
	variant string
}

func (r *recordingImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	r.path = path
	r.variant = variant
	return "resolved://" + path
}

func (r *recordingImageResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(context.Background(), path, variant)
	}
	return out
}
