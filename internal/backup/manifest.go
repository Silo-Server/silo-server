package backup

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

const (
	manifestFormat  = "silo-backup"
	manifestVersion = 1
)

// Status is what the Backups page shows for one folder.
type Status string

const (
	// StatusComplete: manifest present, checksums recorded, table of contents
	// readable, every referenced blob copied. It does not claim the backup was
	// restored; that is what `silo backup verify` checks.
	StatusComplete Status = "complete"
	// StatusCompleteWithWarnings: as complete, but some referenced blobs were
	// missing when copied. The database and every other blob are still a
	// valid recovery point.
	StatusCompleteWithWarnings Status = "complete_with_warnings"
	// StatusInProgress: no manifest yet while a backup holds the lock.
	StatusInProgress Status = "in_progress"
	// StatusIncomplete: no manifest and nothing running; the sweep deletes it
	// after 24 hours.
	StatusIncomplete Status = "incomplete"
	// StatusUnreadable: the folder has a manifest that could not be read
	// just now (the destination failed, or the read was cut short). It is
	// kept, never swept or rotated, until it reads.
	StatusUnreadable Status = "unreadable"
	// StatusUnverified: a manifest this server cannot authenticate: written
	// under another SECRET_KEY or with none recorded, or one naming this key
	// that does not verify (then nothing in it is shown). A restore from it
	// needs the admin's confirmation, and from a tampered one is refused.
	StatusUnverified Status = "unverified"
)

// Manifest is manifest.json, written after every other object. Readers accept
// every version up to manifestVersion and refuse a newer one.
type Manifest struct {
	// storedSize is the manifest object's own size, when it was read from a
	// destination (readManifest).
	storedSize int64
	// authenticated is set when a read verified the MAC under this
	// server's SECRET_KEY (checkMAC).
	authenticated bool

	Format      string    `json:"format"`
	Version     int       `json:"version"`
	ID          string    `json:"id"`
	Kind        Kind      `json:"kind"`
	ServerID    string    `json:"server_id"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at"`

	SiloVersion  string `json:"silo_version"`
	GooseVersion int64  `json:"goose_version"`
	// AppliedMigrations lists every goose version applied in the snapshot,
	// ascending, without goose's version 0. Migrations run out of order, so
	// a database can hold a version below GooseVersion that a binary does
	// not carry; a full restore refuses one. Manifests written before this
	// field have none, and a restore checks GooseVersion alone.
	AppliedMigrations []int64  `json:"applied_migrations"`
	PostgresVersion   string   `json:"postgres_version"`
	PgDumpVersion     string   `json:"pg_dump_version"`
	ExcludedTableData []string `json:"excluded_table_data"`

	// Objects are named as stored: db.dump.age and so on when Encrypted.
	Objects []ObjectEntry `json:"objects"`
	// Encrypted is false for every object at the default destination. The
	// S3 destination encrypts every object but the manifest with age, under
	// the passphrase secret.BackupPassphrase derives from SECRET_KEY.
	// omitempty keeps an unencrypted manifest's encoding, and so its MAC,
	// what builds from before this field wrote: every field added to the
	// manifest after it first shipped must encode to nothing at its zero
	// value (TestManifestsOfEarlierBuildsStillVerify).
	Encrypted bool `json:"encrypted,omitempty"`
	// KeyCheck is the SECRET_KEY check value (secret.KeyCheckValue). A restore
	// with another key cannot read encrypted settings and credentials.
	KeyCheck string `json:"key_check"`
	// TOCCheck records how the dump's table of contents was read: "pipe"
	// while streaming, or "read_back" from the stored object.
	TOCCheck string `json:"toc_check"`

	Blobs BlobSummary `json:"blobs"`
	// MissingBlobs lists at most maxRecordedMissingBlobs of the blobs the
	// backup found gone; MissingBlobsTotal counts them all when it lists
	// fewer.
	MissingBlobs      []MissingBlob `json:"missing_blobs"`
	MissingBlobsTotal int           `json:"missing_blobs_total,omitempty"`
	// Tables is the core table inventory from the backup's snapshot. Full
	// restore compares the restored database with it, presence and counts.
	Tables []TableCount `json:"tables"`
	// RestoreSet describes restore-set.tar, the sections selective restore
	// reads. Absent when the backup has none (before-upgrade backups).
	RestoreSet *RestoreSetSummary `json:"restore_set,omitempty"`
	// MAC authenticates every other field: HMAC-SHA256, under the key
	// secret.BackupManifestKey derives from SECRET_KEY, of the manifest
	// encoded with MAC empty, in hex. Anyone who can write the destination
	// can change objects and their checksums together; only a server with
	// SECRET_KEY can write a MAC that verifies (checkMAC).
	MAC string `json:"mac,omitempty"`
}

// Errors of a manifest's MAC.
var (
	// ErrManifestTampered refuses a manifest written under this server's
	// SECRET_KEY whose MAC is missing or does not verify: it, or an object
	// it describes, was changed since the backup wrote it.
	ErrManifestTampered = errors.New("the backup's manifest does not verify under this server's SECRET_KEY: it was changed after the backup was written")
)

// macInput is what a manifest's MAC covers: the manifest encoded with MAC
// empty. Encoding the decoded manifest again is stable: every field
// round-trips, and a manifest from a newer format is refused before.
func (m *Manifest) macInput() ([]byte, error) {
	c := *m
	c.MAC = ""
	return json.Marshal(&c)
}

// withEncryptedFalse is macInput's encoding of an unencrypted manifest as
// builds wrote it while `encrypted` had no omitempty: "encrypted":false
// right before "key_check", which follows it in Manifest. Inside a string
// the encoder escapes the quote, so the first match is that field's.
func withEncryptedFalse(data []byte) []byte {
	const next = `,"key_check":`
	i := bytes.Index(data, []byte(next))
	if i < 0 {
		return data
	}
	return slices.Concat(data[:i], []byte(`,"encrypted":false`), data[i:])
}

// sign sets m.MAC under key; a nil key leaves m unsigned.
func (m *Manifest) sign(key []byte) error {
	if key == nil {
		return nil
	}
	data, err := m.macInput()
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	m.MAC = hex.EncodeToString(mac.Sum(nil))
	return nil
}

// checkMAC authenticates m for a server whose SECRET_KEY has check value
// keyCheck and manifest key key, as a strict state machine:
//
//   - a manifest naming this key's check value must carry a MAC that
//     verifies (ErrManifestTampered otherwise, a missing MAC included);
//   - one without a check value or a MAC, or naming another key, cannot be
//     authenticated here: authenticated is false, and the caller refuses it
//     unless the operator confirmed (a full restore's
//     --accept-lost-credentials, a selective restore's confirmation).
//
// Every Silo that writes manifests signs them, so a legitimate one from
// this server always verifies. Without a key (key nil) nothing can be
// authenticated.
func (m *Manifest) checkMAC(key []byte, keyCheck string) (authenticated bool, err error) {
	if key == nil || keyCheck == "" || m.KeyCheck != keyCheck {
		return false, nil
	}
	want, err := hex.DecodeString(m.MAC)
	if err != nil || len(want) != sha256.Size {
		return false, ErrManifestTampered
	}
	data, err := m.macInput()
	if err != nil {
		return false, err
	}
	verifies := func(data []byte) bool {
		mac := hmac.New(sha256.New, key)
		mac.Write(data)
		return hmac.Equal(mac.Sum(nil), want)
	}
	if !verifies(data) && (m.Encrypted || !verifies(withEncryptedFalse(data))) {
		return false, ErrManifestTampered
	}
	return true, nil
}

// RestoreSetSummary is the manifest's record of restore-set.tar: its format
// version and how many items each section holds.
type RestoreSetSummary struct {
	FormatVersion int            `json:"format_version"`
	Sections      map[string]int `json:"sections"`
	// Omitted lists items the restore set could not carry; the backup is
	// complete with warnings.
	Omitted []OmittedItem `json:"omitted,omitempty"`
	// IndexSHA256 is the digest of the restore set's index, its first
	// entry. With the manifest authenticated, a read checks the index
	// against it before decoding it, and the archive against its object
	// digest before decoding any document.
	IndexSHA256 string `json:"index_sha256,omitempty"`
}

// ObjectEntry is one stored object: its size and the SHA-256 of the bytes as
// stored.
type ObjectEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BlobSummary struct {
	Count             int   `json:"count"`
	Bytes             int64 `json:"bytes"`
	IncludesSubtitles bool  `json:"includes_subtitles"`
}

// MissingBlob is a key the snapshot referenced that was gone when copied.
type MissingBlob struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	// Source names what referenced it, such as "library poster".
	Source string `json:"source"`
}

// TableCount is one inventory row: whether the table existed in the snapshot
// and, if it did, its row count.
type TableCount struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Rows    int64  `json:"rows"`
}

// Status is complete, or complete with warnings when blobs were missing.
func (m *Manifest) Status() Status {
	if len(m.MissingBlobs) > 0 || (m.RestoreSet != nil && len(m.RestoreSet.Omitted) > 0) {
		return StatusCompleteWithWarnings
	}
	return StatusComplete
}

// HasBlobs reports whether the backup has blobs.tar. A before-upgrade
// backup is the database only: migrations do not touch blobs, so a full
// restore from it leaves the files in place.
func (m *Manifest) HasBlobs() bool { return !m.Kind.databaseOnly() }

// Size is the sum of the stored objects, manifest excluded.
func (m *Manifest) Size() int64 {
	var total int64
	for _, o := range m.Objects {
		total += o.Size
	}
	return total
}

// object finds the object of base (db.dump, blobs.tar, restore-set.tar)
// under its stored name.
func (m *Manifest) object(base string) (ObjectEntry, bool) {
	name := storedName(base, m.Encrypted)
	for _, o := range m.Objects {
		if o.Name == name {
			return o, true
		}
	}
	return ObjectEntry{}, false
}

// ParseManifest decodes manifest.json and refuses a format it cannot read or
// a manifest whose contents a restore could not check against: a restore's
// verification is only as strong as the inventory and checksums here.
// MaxManifestBytes bounds a manifest: what readers read of one, and what
// a backup may write.
const MaxManifestBytes = 16 << 20

// maxRecordedMissingBlobs bounds the missing blobs a manifest lists, so a
// store that lost most of its objects cannot grow the manifest past
// MaxManifestBytes.
const maxRecordedMissingBlobs = 10_000

func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Format != manifestFormat {
		return nil, fmt.Errorf("manifest: format %q is not a Silo backup", m.Format)
	}
	if m.Version < 1 || m.Version > manifestVersion {
		return nil, fmt.Errorf("manifest: version %d is from a newer Silo; upgrade this server first", m.Version)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (m *Manifest) validate() error {
	_, kind, ok := ParseFolderName(m.ID)
	if !ok {
		return fmt.Errorf("id %q is not a backup id", m.ID)
	}
	if m.Kind != kind {
		return fmt.Errorf("kind %q does not match id %q", m.Kind, m.ID)
	}
	if !validServerID(m.ServerID) {
		return fmt.Errorf("server id %q is not valid", m.ServerID)
	}
	if m.GooseVersion < 0 {
		return fmt.Errorf("schema version %d is negative", m.GooseVersion)
	}
	if err := validateApplied(m.AppliedMigrations, m.GooseVersion); err != nil {
		return err
	}
	if !m.HasBlobs() && (m.RestoreSet != nil || m.Blobs.Count != 0 || len(m.MissingBlobs) != 0) {
		return fmt.Errorf("a %s backup holds the database only", m.Kind)
	}
	if err := validateObjects(m.Objects, m.HasBlobs(), m.RestoreSet != nil, m.Encrypted); err != nil {
		return err
	}
	if err := m.RestoreSet.validate(); err != nil {
		return err
	}
	if m.Blobs.Count < 0 || m.Blobs.Bytes < 0 {
		return errors.New("blob totals are negative")
	}
	for _, b := range m.MissingBlobs {
		if b.Store != storeAssets && b.Store != storeOperational {
			return fmt.Errorf("missing blob %q names unknown store %q", b.Key, b.Store)
		}
	}
	return validateInventory(m.Tables)
}

// validateObjects requires exactly db.dump, blobs.tar unless the backup is
// database-only, and restore-set.tar when the manifest describes one, each
// once, with a size and a SHA-256; each named .age when encrypted.
func validateObjects(objects []ObjectEntry, blobs, restoreSet, encrypted bool) error {
	want := map[string]bool{storedName(DumpName, encrypted): false}
	if blobs {
		want[storedName(BlobsName, encrypted)] = false
	}
	if restoreSet {
		want[storedName(RestoreSetName, encrypted)] = false
	}
	for _, o := range objects {
		seen, known := want[o.Name]
		switch {
		case !known:
			return fmt.Errorf("object %q is not part of a backup", o.Name)
		case seen:
			return fmt.Errorf("object %q is listed twice", o.Name)
		case o.Size < 0:
			return fmt.Errorf("object %q has a negative size", o.Name)
		case !sha256Hex.MatchString(o.SHA256):
			return fmt.Errorf("object %q has no valid SHA-256", o.Name)
		}
		want[o.Name] = true
	}
	for name, seen := range want {
		if !seen {
			return fmt.Errorf("object %q is not listed", name)
		}
	}
	return nil
}

// validate checks a restore set summary: a format version, and a
// non-negative count for each section. Sections must be ones this server
// knows unless the format is newer than it reads; the restore set reader
// refuses that format with its own message, and the backup still lists.
func (r *RestoreSetSummary) validate() error {
	if r == nil {
		return nil
	}
	if r.FormatVersion < 1 {
		return fmt.Errorf("restore set format version %d is not valid", r.FormatVersion)
	}
	if r.IndexSHA256 != "" && !sha256Hex.MatchString(r.IndexSHA256) {
		return errors.New("restore set index digest is not valid")
	}
	for id, count := range r.Sections {
		if _, known := sectionByID(id); !known && (r.FormatVersion <= restoreSetVersion || !validSectionID(id)) {
			return fmt.Errorf("restore set section %q is not known", id)
		}
		if count < 0 {
			return fmt.Errorf("restore set section %q has a negative count", id)
		}
	}
	return nil
}

// validateApplied requires recorded migrations to be ascending, positive,
// and to end at the recorded schema version.
func validateApplied(applied []int64, goose int64) error {
	if applied == nil {
		return nil
	}
	var last int64
	for _, v := range applied {
		if v <= last {
			return errors.New("applied migrations are not ascending positive versions")
		}
		last = v
	}
	if last != goose {
		return fmt.Errorf("applied migrations end at %d, the schema version is %d", last, goose)
	}
	return nil
}

// validateInventory requires exactly the core tables, once each: present
// with a row count, or absent with none.
func validateInventory(tables []TableCount) error {
	want := make(map[string]bool, len(inventoryTables))
	for _, name := range inventoryTables {
		want[name] = false
	}
	for _, t := range tables {
		seen, known := want[t.Name]
		switch {
		case !known:
			return fmt.Errorf("inventory table %q is not a core table", t.Name)
		case seen:
			return fmt.Errorf("inventory table %q is listed twice", t.Name)
		case t.Rows < 0, !t.Present && t.Rows != 0:
			return fmt.Errorf("inventory table %q has an impossible count", t.Name)
		}
		want[t.Name] = true
	}
	for _, name := range inventoryTables {
		if !want[name] {
			return fmt.Errorf("inventory table %q is not listed", name)
		}
	}
	return nil
}
