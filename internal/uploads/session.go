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
	ErrChunkBusy        = errors.New("upload chunk already in progress")
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
	// detached holds sessions removed from `sessions` (canceled or expired)
	// whose chunk writers are still draining a request body into the spool.
	// They keep holding real disk and a connection until the writer finishes
	// or hits the read deadline, so callers enforcing admission caps must
	// count them (DetachedWriterSessions) or a cancel-and-recreate loop could
	// stack unbounded live writers behind a fixed session cap.
	detached map[*session]struct{}
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
	// writing marks chunk indexes with a body copy in flight. Body I/O happens
	// outside the manager mutex (a slow client must not stall every other
	// session), so these flags are what stops a duplicate concurrent write to
	// the same offset and keeps Complete/Cancel from consuming a session
	// while bytes are still landing in its file.
	writing map[int]struct{}
}

func (s *session) writingCount() int {
	return len(s.writing)
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
		detached:     make(map[*session]struct{}),
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

// PutChunk streams one chunk into the session file. The body copy runs
// OUTSIDE the manager mutex — network reads from one slow client must not
// serialize every other session's chunk writes, completes, and cancels behind
// it. A per-chunk in-flight flag prevents duplicate concurrent writes to the
// same offset. Each begun and each accepted chunk refreshes the session
// expiry, making the TTL an idle timeout rather than an absolute deadline so
// a slow-but-progressing upload stays alive.
func (m *Manager) PutChunk(ctx context.Context, id string, index int, body io.Reader, contentLength int64) (SessionInfo, error) {
	if body == nil {
		return SessionInfo{}, fmt.Errorf("%w: chunk body is required", ErrInvalidChunk)
	}

	m.mu.Lock()
	m.cleanupExpiredLocked(m.now())

	s, err := m.getLocked(id)
	if err != nil {
		m.mu.Unlock()
		return SessionInfo{}, err
	}
	if s.completed {
		m.mu.Unlock()
		return SessionInfo{}, ErrAlreadyCompleted
	}
	if index < 0 || index >= s.totalChunks {
		m.mu.Unlock()
		return SessionInfo{}, fmt.Errorf("%w: chunk index is out of range", ErrInvalidChunk)
	}

	expectedSize := s.expectedChunkSize(index)
	if contentLength >= 0 && contentLength != expectedSize {
		m.mu.Unlock()
		return SessionInfo{}, fmt.Errorf("%w: chunk size must be %d bytes", ErrInvalidChunk, expectedSize)
	}
	if expectedSize > m.maxChunkSize {
		m.mu.Unlock()
		return SessionInfo{}, fmt.Errorf("%w: maximum chunk size is %d bytes", ErrTooLarge, m.maxChunkSize)
	}
	if s.received[index] {
		info := s.info()
		m.mu.Unlock()
		return info, nil
	}
	if _, inFlight := s.writing[index]; inFlight {
		m.mu.Unlock()
		return SessionInfo{}, ErrChunkBusy
	}
	if s.writing == nil {
		s.writing = make(map[int]struct{})
	}
	s.writing[index] = struct{}{}
	s.expiresAt = m.now().Add(m.ttl)
	path := s.path
	offset := int64(index) * s.chunkSize
	m.mu.Unlock()

	written, copyErr := copyChunkBody(ctx, path, offset, body, expectedSize)

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(s.writing, index)
	// The session may have been canceled or expired while the copy ran. Its
	// directory removal was deferred to the last finishing writer (see
	// removeDetachedDirLocked); report the session gone either way.
	if s.completed {
		return SessionInfo{}, ErrAlreadyCompleted
	}
	if m.sessions[id] != s {
		m.removeDetachedDirLocked(s)
		return SessionInfo{}, ErrNotFound
	}
	if copyErr != nil {
		return SessionInfo{}, copyErr
	}
	s.received[index] = true
	s.receivedChunks++
	s.receivedBytes += written
	s.expiresAt = m.now().Add(m.ttl)
	return s.info(), nil
}

// removeDetachedDirLocked reclaims a detached session once its last in-flight
// writer has returned: drops it from the detached count and removes the spool
// directory. Safe to call for every finishing writer of a dropped session.
func (m *Manager) removeDetachedDirLocked(s *session) {
	if s.writingCount() != 0 {
		return
	}
	delete(m.detached, s)
	_ = os.RemoveAll(s.dir)
}

// copyChunkBody writes exactly expectedSize bytes from body at offset,
// rejecting short or oversized chunks. Runs without the manager lock held.
func copyChunkBody(ctx context.Context, path string, offset int64, body io.Reader, expectedSize int64) (int64, error) {
	file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open upload session file: %w", err)
	}
	defer file.Close()

	writer := io.NewOffsetWriter(file, offset)
	written, err := io.Copy(writer, io.LimitReader(body, expectedSize))
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if written != expectedSize {
		return 0, fmt.Errorf("%w: chunk size must be %d bytes", ErrInvalidChunk, expectedSize)
	}
	var extra [1]byte
	extraBytes, err := body.Read(extra[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if extraBytes > 0 {
		return 0, fmt.Errorf("%w: chunk size must be %d bytes", ErrInvalidChunk, expectedSize)
	}
	return written, nil
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

// Peek reports the session's current state without mutating it beyond the
// shared expiry cleanup. Callers holding per-session state outside the
// manager (e.g. diagnostics chunked uploads) use it to notice sessions the
// manager has expired so their own bookkeeping can be released too.
func (m *Manager) Peek(id string) (SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.getLocked(id)
	if err != nil {
		return SessionInfo{}, err
	}
	return s.info(), nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return ErrNotFound
	}
	delete(m.sessions, id)
	// With a chunk write in flight the file handle is still open; park the
	// session as detached — still counted by DetachedWriterSessions — and the
	// last finishing writer removes the directory.
	if s.writingCount() == 0 {
		return os.RemoveAll(s.dir)
	}
	m.detached[s] = struct{}{}
	return nil
}

// DetachedWriterSessions counts canceled/expired sessions whose chunk writers
// are still draining request bodies into spool files. Admission caps must
// include them: they hold connections and disk until each writer finishes or
// times out.
func (m *Manager) DetachedWriterSessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.detached)
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
		if s.writingCount() == 0 {
			_ = os.RemoveAll(s.dir)
		} else {
			m.detached[s] = struct{}{}
		}
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
		if s.writingCount() == 0 {
			_ = os.RemoveAll(s.dir)
		} else {
			m.detached[s] = struct{}{}
		}
	}
}

// ReclaimOrphanedDirs deletes session directories under the manager's root
// that no live session owns. Call once at startup: after a process restart
// the in-memory session map is empty, but the previous process's partially
// uploaded spool directories survive on disk and would otherwise accumulate
// forever (no sweep ever discovers them). Returns the number of directories
// removed.
func (m *Manager) ReclaimOrphanedDirs() int {
	m.mu.Lock()
	live := make(map[string]struct{}, len(m.sessions))
	for id := range m.sessions {
		live[id] = struct{}{}
	}
	rootDir := m.rootDir
	m.mu.Unlock()

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := live[entry.Name()]; ok {
			continue
		}
		if os.RemoveAll(filepath.Join(rootDir, entry.Name())) == nil {
			removed++
		}
	}
	return removed
}

// StartExpirySweeper runs cleanup on the given interval until stop is closed.
// Without it, expiry only triggers from later API calls — sessions abandoned
// during a quiet period would otherwise hold their spool bytes indefinitely.
func (m *Manager) StartExpirySweeper(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				m.mu.Lock()
				m.cleanupExpiredLocked(m.now())
				m.mu.Unlock()
			}
		}
	}()
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
