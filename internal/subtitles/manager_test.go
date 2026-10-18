package subtitles

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockSubtitleRepo struct {
	mu      sync.Mutex
	byKey   map[string]*DownloadedSubtitle
	nextID  int
	inserts int
}

func newMockSubtitleRepo() *mockSubtitleRepo {
	return &mockSubtitleRepo{byKey: make(map[string]*DownloadedSubtitle)}
}

func (m *mockSubtitleRepo) InsertDownloadedSubtitle(_ context.Context, sub *DownloadedSubtitle) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inserts++
	m.nextID++
	sub.ID = m.nextID
	sub.CreatedAt = time.Now()
	sub.Revision = 1
	m.byKey[sub.S3Key] = sub
	return nil
}

func (m *mockSubtitleRepo) GetDownloadedSubtitle(_ context.Context, id int) (*DownloadedSubtitle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.byKey {
		if sub.ID == id {
			copy := *sub
			return &copy, nil
		}
	}
	return nil, nil
}

func (m *mockSubtitleRepo) ListDownloadedSubtitles(context.Context, int) ([]DownloadedSubtitle, error) {
	return nil, nil
}

func (m *mockSubtitleRepo) UpdateDownloadedSubtitle(_ context.Context, id int, update SubtitleMetadataUpdate) (*DownloadedSubtitle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.byKey {
		if sub.ID == id {
			if update.Language != nil {
				sub.Language = *update.Language
			}
			if update.ReleaseName != nil {
				sub.ReleaseName = *update.ReleaseName
			}
			if update.HearingImpaired != nil {
				sub.HearingImpaired = *update.HearingImpaired
			}
			if update.ContentSHA256 != "" {
				sub.ContentSHA256 = update.ContentSHA256
			}
			if update.Timing != nil {
				sub.Timing = *update.Timing
			}
			sub.Revision++
			copy := *sub
			return &copy, nil
		}
	}
	return nil, nil
}

func (m *mockSubtitleRepo) DeleteDownloadedSubtitle(context.Context, int) (*DownloadedSubtitle, error) {
	return nil, nil
}

func (m *mockSubtitleRepo) GetDownloadedSubtitleByS3Key(_ context.Context, s3Key string) (*DownloadedSubtitle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sub, ok := m.byKey[s3Key]; ok {
		copy := *sub
		return &copy, nil
	}
	return nil, nil
}

func (m *mockSubtitleRepo) ListProviderConfigs(context.Context) ([]ProviderConfig, error) {
	return nil, nil
}

func (m *mockSubtitleRepo) GetProviderConfig(context.Context, string) (*ProviderConfig, error) {
	return nil, nil
}

func (m *mockSubtitleRepo) UpsertProviderConfig(context.Context, *ProviderConfig) error {
	return nil
}

type mockBlobStore struct {
	mu      sync.Mutex
	keys    map[string][]byte
	puts    int
	deletes int
}

func newMockBlobStore() *mockBlobStore {
	return &mockBlobStore{keys: make(map[string][]byte)}
}

func (m *mockBlobStore) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.keys[key] = append([]byte(nil), data...)
	return nil
}

func (m *mockBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.keys[key]...), nil
}

func (m *mockBlobStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes++
	delete(m.keys, key)
	return nil
}

type stubProvider struct {
	name string
	data []byte
}

func (s stubProvider) Name() string { return s.name }

func (s stubProvider) Search(context.Context, SearchRequest) ([]SubtitleResult, error) {
	return nil, nil
}

func (s stubProvider) Download(context.Context, string) ([]byte, SubtitleFormat, error) {
	return s.data, FormatSRT, nil
}

type failingSearchProvider struct{ stubProvider }

func (failingSearchProvider) Search(context.Context, SearchRequest) ([]SubtitleResult, error) {
	return nil, errors.New("rpc error: code = Unauthenticated desc = api_key=synthetic-secret upstream detail")
}

func TestManagerSearchWarningsOmitProviderErrorText(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	manager := NewManager(newMockSubtitleRepo(), newMockBlobStore())
	manager.RegisterProvider(failingSearchProvider{stubProvider{name: "example"}})
	resp, err := manager.SearchBridge(t.Context(), SearchRequest{Title: "Example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0] != "example: search failed" {
		t.Fatalf("warnings = %q", resp.Warnings)
	}
	if !strings.Contains(logs.String(), "upstream detail") || strings.Contains(logs.String(), "synthetic-secret") {
		t.Fatalf("provider failure log = %q", logs.String())
	}
}

func TestManagerDownloadRejectsOversizedProviderResult(t *testing.T) {
	repo := newMockSubtitleRepo()
	blobs := newMockBlobStore()
	manager := NewManager(repo, blobs)
	manager.RegisterProvider(stubProvider{name: "big", data: make([]byte, MaxUploadSize+1)})
	manager.RegisterProvider(stubProvider{name: "ok", data: []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")})

	_, err := manager.Download(context.Background(), DownloadRequest{MediaFileID: 7, ProviderName: "big", SubtitleID: "1", Language: "en"})
	if !errors.Is(err, ErrSubtitleTooLarge) {
		t.Fatalf("Download() error = %v, want ErrSubtitleTooLarge", err)
	}
	if repo.inserts != 0 || len(blobs.keys) != 0 {
		t.Fatalf("oversized download stored %d rows and %d blobs, want none", repo.inserts, len(blobs.keys))
	}

	sub, err := manager.Download(context.Background(), DownloadRequest{MediaFileID: 7, ProviderName: "ok", SubtitleID: "2", Language: "en"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if sub.Provider != "ok" || sub.Format != FormatSRT {
		t.Fatalf("stored provider=%q format=%q, want ok srt", sub.Provider, sub.Format)
	}
}

func TestManagerProviderNames(t *testing.T) {
	manager := NewManager(newMockSubtitleRepo(), newMockBlobStore())

	names := manager.ProviderNames()
	if names == nil {
		t.Fatal("ProviderNames() = nil, want non-nil empty slice")
	}
	if len(names) != 0 {
		t.Fatalf("ProviderNames() = %v, want empty", names)
	}

	manager.RegisterProvider(stubProvider{name: "subdl"})
	manager.RegisterProvider(stubProvider{name: "opensubtitles"})
	manager.RegisterProvider(stubProvider{name: "subsource"})

	want := []string{"opensubtitles", "subdl", "subsource"}
	names = manager.ProviderNames()
	if len(names) != len(want) {
		t.Fatalf("ProviderNames() = %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("ProviderNames() = %v, want %v (sorted)", names, want)
		}
	}

	manager.RemoveProvider("subdl")
	names = manager.ProviderNames()
	if len(names) != 2 || names[0] != "opensubtitles" || names[1] != "subsource" {
		t.Fatalf("ProviderNames() after removal = %v, want [opensubtitles subsource]", names)
	}
}

func TestManagerUploadDedupesIdenticalContent(t *testing.T) {
	repo := newMockSubtitleRepo()
	s3 := newMockBlobStore()
	manager := NewManager(repo, s3)

	data := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	first, err := manager.Upload(context.Background(), UploadRequest{
		MediaFileID: 7,
		Language:    "en",
		Filename:    "a.srt",
		Data:        data,
	})
	if err != nil {
		t.Fatalf("first Upload() error = %v", err)
	}

	if first.Provider != ProviderUpload || first.Format != FormatSRT {
		t.Fatalf("uploaded provider=%q format=%q, want upload srt", first.Provider, first.Format)
	}

	second, err := manager.Upload(context.Background(), UploadRequest{
		MediaFileID: 7,
		Language:    "en",
		Filename:    "b.srt",
		Data:        data,
	})
	if err != nil {
		t.Fatalf("second Upload() error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("dedup failed: ids %d vs %d", first.ID, second.ID)
	}
	if repo.inserts != 1 {
		t.Fatalf("inserts = %d, want 1", repo.inserts)
	}
	if s3.puts != 1 {
		t.Fatalf("puts = %d, want 1", s3.puts)
	}
}

func TestManagerUploadRejectsUnsupportedFormat(t *testing.T) {
	manager := NewManager(newMockSubtitleRepo(), newMockBlobStore())
	_, err := manager.Upload(context.Background(), UploadRequest{
		MediaFileID: 1,
		Language:    "en",
		Filename:    "notes.txt",
		Data:        []byte("hello"),
	})
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func TestManagerUploadRejectsOversizedFile(t *testing.T) {
	manager := NewManager(newMockSubtitleRepo(), newMockBlobStore())
	data := make([]byte, MaxUploadSize+1)
	_, err := manager.Upload(context.Background(), UploadRequest{
		MediaFileID: 1,
		Language:    "en",
		Filename:    "big.srt",
		Data:        data,
	})
	if err == nil {
		t.Fatal("expected error for oversized file")
	}
}

func (m *mockSubtitleRepo) GetDownloadedSubtitleByContent(_ context.Context, content *DownloadedSubtitle) (*DownloadedSubtitle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.byKey {
		if content.ContentSHA256 != "" && sub.MediaFileID == content.MediaFileID && sub.Provider == content.Provider && sub.Language == content.Language && sub.Format == content.Format && sub.ContentSHA256 == content.ContentSHA256 {
			copy := *sub
			return &copy, nil
		}
	}
	return nil, nil
}
