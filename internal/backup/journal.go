package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Swap-mode journal steps. The rename and drop steps are informational: what
// happened to the databases is read back by OID (swap.observe), never
// inferred from these flags, because a crash can land between a statement and
// the record of it.
const (
	stepLiveClosed       = "live_closed"
	stepBlobsWritten     = "blobs_written"
	stepRenameLiveIntent = "rename_live_intent"
	stepLiveRenamed      = "live_renamed"
	stepRenameRestIntent = "rename_restored_intent"
	stepRestoredRenamed  = "restored_renamed"
	stepLiveReopened     = "live_reopened"

	stepRollbackRename   = "rollback_rename"
	stepRollbackBlobs    = "rollback_blobs"
	stepRollbackDrop     = "rollback_drop"
	stepRollbackReopened = "rollback_reopened"
)

// journalRecord is one line of the journal. The first line carries the
// header; each later line is a completed step or a blob about to be written.
type journalRecord struct {
	Header *journalHeader `json:"header,omitempty"`
	Step   string         `json:"step,omitempty"`
	Blob   *journalBlob   `json:"blob,omitempty"`
}

type journalHeader struct {
	Time             string `json:"time"`
	ServerID         string `json:"server_id"`
	BackupID         string `json:"backup_id"`
	LiveDatabase     string `json:"live_database"`
	RestoredDatabase string `json:"restored_database"`
	BeforeDatabase   string `json:"before_database"`
	// LiveOID and RestoredOID identify the two databases whatever they are
	// named. Recovery reads their current names from pg_database.
	LiveOID        uint32 `json:"live_oid"`
	RestoredOID    uint32 `json:"restored_oid"`
	PreservePrefix string `json:"preserve_prefix"`
	// Stores is the live blob storage, recorded before anything changes so
	// recovery never has to open a closed database to find it. Credentials
	// in it are sealed with SECRET_KEY.
	Stores *StoreConfig `json:"stores,omitempty"`
}

// journalBlob is recorded before the restored bytes are written, so a
// rollback after any crash knows every key that might have changed. A key
// that already existed was first copied to PreservePrefix+key in its store.
type journalBlob struct {
	Store     string `json:"store"`
	Key       string `json:"key"`
	Preserved bool   `json:"preserved"`
}

// journal is the swap's record of completed steps. Every record is appended
// and synced to a local file before the step it describes proceeds; the
// whole journal is also copied into the blob store at each phase boundary.
type journal struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	header journalHeader
	steps  map[string]bool
	blobs  []journalBlob
	lines  bytes.Buffer
	// mirror writes the whole journal into the store; nil to skip.
	mirror func(ctx context.Context, data []byte) error
}

// JournalPath is where a swap restore keeps its local journal.
func JournalPath(dir, stamp string) string {
	return filepath.Join(dir, "silo-restore-"+stamp+".journal")
}

func createJournal(path string, header journalHeader) (*journal, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: the operator's working directory
	if err != nil {
		return nil, fmt.Errorf("creating the restore journal: %w", err)
	}
	j := &journal{path: path, file: file, header: header, steps: map[string]bool{}}
	if err := j.append(journalRecord{Header: &header}); err != nil {
		_ = file.Close()
		return nil, err
	}
	// The file's own fsync does not make its name durable. Without the
	// directory's, a host crash could lose the journal after databases were
	// renamed or files overwritten, so the swap does not start until both
	// are on disk.
	if err := syncDir(filepath.Dir(path)); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("making the restore journal durable: %w", err)
	}
	return j, nil
}

// syncDir fsyncs a directory, making the names in it durable. A variable so
// a test can make it fail.
var syncDir = func(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the journal's own directory
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	if err := d.Close(); syncErr == nil {
		syncErr = err
	}
	return syncErr
}

// errCorruptJournal reports damage before the journal's last record. Only a
// torn final record, from a crash in the middle of its write, is repaired.
var errCorruptJournal = errors.New("the restore journal is damaged before its last record")

// openJournal reads an existing journal for --recover and reopens it for
// appending. A torn final record is cut off and the file synced before
// anything is appended, so the next record never lands after damaged bytes.
// The record a torn line held was never acted on: every record is synced
// before the step it describes proceeds.
func openJournal(path string) (*journal, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the journal the operator named with --recover
	if err != nil {
		return nil, fmt.Errorf("reading the restore journal: %w", err)
	}
	j := &journal{path: path, steps: map[string]bool{}}
	valid := 0
	for offset := 0; offset < len(data); {
		end := bytes.IndexByte(data[offset:], '\n')
		if end < 0 {
			break // An unterminated last line is a torn write.
		}
		line := data[offset : offset+end]
		var rec journalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			if offset+end+1 < len(data) {
				return nil, fmt.Errorf("%w: line at byte %d: %w", errCorruptJournal, offset, err)
			}
			break // The last line is torn.
		}
		if offset == 0 && rec.Header == nil {
			return nil, fmt.Errorf("%w: it does not start with a header", errCorruptJournal)
		}
		j.apply(rec)
		j.lines.Write(line)
		j.lines.WriteByte('\n')
		offset += end + 1
		valid = offset
	}
	if j.header.LiveDatabase == "" {
		return nil, errors.New("the restore journal has no header")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: the journal the operator named with --recover
	if err != nil {
		return nil, err
	}
	if valid < len(data) {
		slog.Warn("cutting a torn last record from the restore journal", "component", "backup", "bytes", len(data)-valid)
		if err := file.Truncate(int64(valid)); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("repairing the restore journal: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("repairing the restore journal: %w", err)
		}
	}
	j.file = file
	return j, nil
}

func (j *journal) apply(rec journalRecord) {
	switch {
	case rec.Header != nil:
		j.header = *rec.Header
	case rec.Blob != nil:
		j.blobs = append(j.blobs, *rec.Blob)
	case rec.Step != "":
		j.steps[rec.Step] = true
	}
}

func (j *journal) append(rec journalRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	line, _ := json.Marshal(rec)
	line = append(line, '\n')
	if _, err := j.file.Write(line); err != nil {
		return fmt.Errorf("writing the restore journal: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("syncing the restore journal: %w", err)
	}
	j.lines.Write(line)
	j.apply(rec)
	return nil
}

func (j *journal) step(ctx context.Context, name string) error {
	if err := j.append(journalRecord{Step: name}); err != nil {
		return err
	}
	if err := j.sync(ctx); err != nil {
		slog.WarnContext(ctx, "copying the restore journal into the store failed; the local journal is complete", "component", "backup", "error", err)
	}
	return nil
}

func (j *journal) blob(b journalBlob) error { return j.append(journalRecord{Blob: &b}) }

func (j *journal) has(step string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.steps[step]
}

// sync copies the journal into the store. A failure is reported but does not
// stop the restore: the local journal is the one --recover reads.
func (j *journal) sync(ctx context.Context) error {
	if j.mirror == nil {
		return nil
	}
	j.mu.Lock()
	data := append([]byte(nil), j.lines.Bytes()...)
	j.mu.Unlock()
	return j.mirror(ctx, data)
}

func (j *journal) close() {
	if j.file != nil {
		_ = j.file.Close()
	}
}
