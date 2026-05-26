package subtitles

import (
	"context"
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
	m.byKey[sub.S3Key] = sub
	return nil
}

func (m *mockSubtitleRepo) GetDownloadedSubtitle(context.Context, int) (*DownloadedSubtitle, error) {
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
			sub.Language = update.Language
			sub.ReleaseName = update.ReleaseName
			sub.HearingImpaired = update.HearingImpaired
			if sub.S3Key != update.S3Key {
				delete(m.byKey, sub.S3Key)
				sub.S3Key = update.S3Key
				m.byKey[sub.S3Key] = sub
			}
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

type mockS3Client struct {
	mu      sync.Mutex
	keys    map[string][]byte
	puts    int
	deletes int
}

func newMockS3Client() *mockS3Client {
	return &mockS3Client{keys: make(map[string][]byte)}
}

func (m *mockS3Client) PutObject(_ context.Context, _, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.keys[key] = append([]byte(nil), data...)
	return nil
}

func (m *mockS3Client) GetObject(_ context.Context, _, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.keys[key]...), nil
}

func (m *mockS3Client) DeleteObject(_ context.Context, _, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes++
	delete(m.keys, key)
	return nil
}

func TestManagerUploadStoresSubtitle(t *testing.T) {
	repo := newMockSubtitleRepo()
	s3 := newMockS3Client()
	manager := NewManager(repo, s3, "test-bucket")

	data := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	sub, err := manager.Upload(context.Background(), UploadRequest{
		MediaFileID: 42,
		Language:    "en",
		Filename:    "custom.en.srt",
		Data:        data,
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if sub.Provider != ProviderUpload {
		t.Fatalf("provider = %q, want %q", sub.Provider, ProviderUpload)
	}
	if sub.Format != FormatSRT {
		t.Fatalf("format = %q, want srt", sub.Format)
	}
	if repo.inserts != 1 {
		t.Fatalf("inserts = %d, want 1", repo.inserts)
	}
	if s3.puts != 1 {
		t.Fatalf("puts = %d, want 1", s3.puts)
	}
}

func TestManagerUploadDedupesIdenticalContent(t *testing.T) {
	repo := newMockSubtitleRepo()
	s3 := newMockS3Client()
	manager := NewManager(repo, s3, "test-bucket")

	data := []byte("duplicate content")
	first, err := manager.Upload(context.Background(), UploadRequest{
		MediaFileID: 7,
		Language:    "en",
		Filename:    "a.srt",
		Data:        data,
	})
	if err != nil {
		t.Fatalf("first Upload() error = %v", err)
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
	manager := NewManager(newMockSubtitleRepo(), newMockS3Client(), "test-bucket")
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
	manager := NewManager(newMockSubtitleRepo(), newMockS3Client(), "test-bucket")
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
