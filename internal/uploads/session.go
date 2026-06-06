package uploads

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultSessionTTL = 2 * time.Hour
	defaultMaxSize    = 256 << 20
	defaultChunkSize  = 8 << 20
)

var (
	ErrAlreadyCompleted = errors.New("upload session is already completed")
	ErrExpired          = errors.New("upload session expired")
	ErrIncomplete       = errors.New("upload session is incomplete")
	ErrInvalidChunk     = errors.New("invalid upload chunk")
	ErrInvalidRequest   = errors.New("invalid upload session request")
	ErrNotFound         = errors.New("upload session not found")
	ErrTooLarge         = errors.New("upload exceeds maximum size")
)

type ManagerOptions struct {
	RootDir      string
	TTL          time.Duration
	MaxSize      int64
	MaxChunkSize int64
	Now          func() time.Time
}

type CreateRequest struct {
	Filename  string
	SizeBytes int64
	ChunkSize int64
}

type SessionInfo struct {
	ID             string
	Filename       string
	SizeBytes      int64
	ChunkSize      int64
	TotalChunks    int
	ReceivedChunks int
	ReceivedBytes  int64
	Complete       bool
	ExpiresAt      time.Time
}

type CompletedUpload struct {
	ID        string
	Filename  string
	SizeBytes int64
	Path      string
	Cleanup   func()
}

type Manager struct {
	mu           sync.Mutex
	rootDir      string
	ttl          time.Duration
	maxSize      int64
	maxChunkSize int64
	now          func() time.Time
	sessions     map[string]*session
}

type session struct {
	id             string
	filename       string
	sizeBytes      int64
	chunkSize      int64
	totalChunks    int
	received       []bool
	receivedChunks int
	receivedBytes  int64
	path           string
	dir            string
	expiresAt      time.Time
	completed      bool
}

func NewManager(opts ManagerOptions) *Manager {
	rootDir := opts.RootDir
	if strings.TrimSpace(rootDir) == "" {
		rootDir = filepath.Join(os.TempDir(), "silo-uploads")
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	maxSize := opts.MaxSize
	if maxSize <= 0 {
		maxSize = defaultMaxSize
	}
	maxChunkSize := opts.MaxChunkSize
	if maxChunkSize <= 0 {
		maxChunkSize = defaultChunkSize
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		rootDir:      rootDir,
		ttl:          ttl,
		maxSize:      maxSize,
		maxChunkSize: maxChunkSize,
		now:          now,
		sessions:     make(map[string]*session),
	}
}

func (m *Manager) MaxChunkSize() int64 {
	return m.maxChunkSize
}

func (m *Manager) Create(req CreateRequest) (SessionInfo, error) {
	filename := filepath.Base(strings.TrimSpace(req.Filename))
	if filename == "." || filename == string(filepath.Separator) || filename == "" {
		filename = "upload.bin"
	}
	if req.SizeBytes <= 0 {
		return SessionInfo{}, fmt.Errorf("%w: size_bytes must be positive", ErrInvalidRequest)
	}
	if req.SizeBytes > m.maxSize {
		return SessionInfo{}, fmt.Errorf("%w: maximum size is %d bytes", ErrTooLarge, m.maxSize)
	}

	chunkSize := req.ChunkSize
	if chunkSize <= 0 {
		chunkSize = m.maxChunkSize
	}
	if chunkSize > m.maxChunkSize {
		return SessionInfo{}, fmt.Errorf("%w: chunk_size must not exceed %d bytes", ErrInvalidRequest, m.maxChunkSize)
	}

	totalChunks64 := (req.SizeBytes + chunkSize - 1) / chunkSize
	if totalChunks64 <= 0 || totalChunks64 > math.MaxInt32 {
		return SessionInfo{}, fmt.Errorf("%w: invalid total chunk count", ErrInvalidRequest)
	}

	if err := os.MkdirAll(m.rootDir, 0o700); err != nil {
		return SessionInfo{}, fmt.Errorf("create upload root: %w", err)
	}

	id, err := newID()
	if err != nil {
		return SessionInfo{}, err
	}
	dir := filepath.Join(m.rootDir, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return SessionInfo{}, fmt.Errorf("create upload session directory: %w", err)
	}

	path := filepath.Join(dir, "upload.bin")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return SessionInfo{}, fmt.Errorf("create upload session file: %w", err)
	}
	if err := file.Truncate(req.SizeBytes); err != nil {
		_ = file.Close()
		_ = os.RemoveAll(dir)
		return SessionInfo{}, fmt.Errorf("size upload session file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return SessionInfo{}, fmt.Errorf("close upload session file: %w", err)
	}

	now := m.now()
	s := &session{
		id:          id,
		filename:    filename,
		sizeBytes:   req.SizeBytes,
		chunkSize:   chunkSize,
		totalChunks: int(totalChunks64),
		received:    make([]bool, int(totalChunks64)),
		path:        path,
		dir:         dir,
		expiresAt:   now.Add(m.ttl),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked(now)
	m.sessions[id] = s
	return s.info(), nil
}

func (m *Manager) PutChunk(ctx context.Context, id string, index int, body io.Reader, contentLength int64) (SessionInfo, error) {
	if body == nil {
		return SessionInfo{}, fmt.Errorf("%w: chunk body is required", ErrInvalidChunk)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked(m.now())

	s, err := m.getLocked(id)
	if err != nil {
		return SessionInfo{}, err
	}
	if s.completed {
		return SessionInfo{}, ErrAlreadyCompleted
	}
	if index < 0 || index >= s.totalChunks {
		return SessionInfo{}, fmt.Errorf("%w: chunk index is out of range", ErrInvalidChunk)
	}

	expectedSize := s.expectedChunkSize(index)
	if contentLength >= 0 && contentLength != expectedSize {
		return SessionInfo{}, fmt.Errorf("%w: chunk size must be %d bytes", ErrInvalidChunk, expectedSize)
	}
	if expectedSize > m.maxChunkSize {
		return SessionInfo{}, fmt.Errorf("%w: maximum chunk size is %d bytes", ErrTooLarge, m.maxChunkSize)
	}
	if s.received[index] {
		return s.info(), nil
	}

	file, err := os.OpenFile(s.path, os.O_WRONLY, 0o600)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("open upload session file: %w", err)
	}
	defer file.Close()

	writer := io.NewOffsetWriter(file, int64(index)*s.chunkSize)
	written, err := io.Copy(writer, io.LimitReader(body, expectedSize))
	if err != nil {
		return SessionInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return SessionInfo{}, err
	}
	if written != expectedSize {
		return SessionInfo{}, fmt.Errorf("%w: chunk size must be %d bytes", ErrInvalidChunk, expectedSize)
	}
	var extra [1]byte
	extraBytes, err := body.Read(extra[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return SessionInfo{}, err
	}
	if extraBytes > 0 {
		return SessionInfo{}, fmt.Errorf("%w: chunk size must be %d bytes", ErrInvalidChunk, expectedSize)
	}

	s.received[index] = true
	s.receivedChunks++
	s.receivedBytes += written
	return s.info(), nil
}

func (m *Manager) Complete(id string) (*CompletedUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked(m.now())

	s, err := m.getLocked(id)
	if err != nil {
		return nil, err
	}
	if s.completed {
		return nil, ErrAlreadyCompleted
	}
	if !s.isComplete() {
		return nil, ErrIncomplete
	}
	s.completed = true
	delete(m.sessions, id)

	return &CompletedUpload{
		ID:        s.id,
		Filename:  s.filename,
		SizeBytes: s.sizeBytes,
		Path:      s.path,
		Cleanup: func() {
			_ = os.RemoveAll(s.dir)
		},
	}, nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return ErrNotFound
	}
	delete(m.sessions, id)
	return os.RemoveAll(s.dir)
}

func (m *Manager) getLocked(id string) (*session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrNotFound
	}
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !m.now().Before(s.expiresAt) {
		delete(m.sessions, id)
		_ = os.RemoveAll(s.dir)
		return nil, ErrExpired
	}
	return s, nil
}

func (m *Manager) cleanupExpiredLocked(now time.Time) {
	for id, s := range m.sessions {
		if now.Before(s.expiresAt) {
			continue
		}
		delete(m.sessions, id)
		_ = os.RemoveAll(s.dir)
	}
}

func (s *session) isComplete() bool {
	return s.receivedChunks == s.totalChunks && s.receivedBytes == s.sizeBytes
}

func (s *session) expectedChunkSize(index int) int64 {
	offset := int64(index) * s.chunkSize
	remaining := s.sizeBytes - offset
	if remaining < s.chunkSize {
		return remaining
	}
	return s.chunkSize
}

func (s *session) info() SessionInfo {
	return SessionInfo{
		ID:             s.id,
		Filename:       s.filename,
		SizeBytes:      s.sizeBytes,
		ChunkSize:      s.chunkSize,
		TotalChunks:    s.totalChunks,
		ReceivedChunks: s.receivedChunks,
		ReceivedBytes:  s.receivedBytes,
		Complete:       s.receivedChunks == s.totalChunks && s.receivedBytes == s.sizeBytes,
		ExpiresAt:      s.expiresAt,
	}
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate upload session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
