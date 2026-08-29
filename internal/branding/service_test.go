package branding

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

type serviceTestSettings map[string]string

func (s serviceTestSettings) Get(_ context.Context, key string) (string, error) {
	return s[key], nil
}

func (s serviceTestSettings) Set(_ context.Context, key, value string) error {
	s[key] = value
	return nil
}

type serviceTestAssetStore struct {
	data         []byte
	reportedSize int64
}

func (*serviceTestAssetStore) WriteImmutable(context.Context, string, []byte, artworkstore.ObjectMetadata) error {
	return nil
}

func (s *serviceTestAssetStore) Open(context.Context, string) (*artworkstore.Object, error) {
	return &artworkstore.Object{
		Info: artworkstore.ObjectInfo{SizeBytes: s.reportedSize},
		Body: io.NopCloser(bytes.NewReader(s.data)),
	}, nil
}

func (s *serviceTestAssetStore) Stat(context.Context, string) (artworkstore.ObjectInfo, error) {
	return artworkstore.ObjectInfo{SizeBytes: s.reportedSize}, nil
}

func TestGetAssetRejectsOversizedStoredObject(t *testing.T) {
	limit := MaxUploadBytes(KindFavicon)
	oversized := bytes.Repeat([]byte("x"), int(limit+1))

	for _, test := range []struct {
		name         string
		reportedSize int64
	}{
		{name: "size metadata", reportedSize: int64(len(oversized))},
		{name: "unavailable size metadata", reportedSize: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := serviceTestSettings{assetSpecs[KindFavicon].settingKey: "external.png"}
			service := NewService(settings, &serviceTestAssetStore{data: oversized, reportedSize: test.reportedSize})

			data, contentType, ref, err := service.GetAsset(t.Context(), KindFavicon)

			if !errors.Is(err, ErrAssetNotConfigured) {
				t.Fatalf("GetAsset error = %v, want ErrAssetNotConfigured", err)
			}
			if err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("GetAsset error = %v, want stored-size limit detail", err)
			}
			if data != nil || contentType != "" || ref != "" {
				t.Fatalf("GetAsset returned data=%d content_type=%q ref=%q on oversized object", len(data), contentType, ref)
			}
		})
	}
}
