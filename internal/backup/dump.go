package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// TOC check modes recorded in the manifest.
const (
	tocCheckPipe     = "pipe"
	tocCheckReadBack = "read_back"
)

// dump streams pg_dump's output into the destination with no local spool,
// hashing the stored bytes, while `pg_restore --list` reads a tee of the
// stream to check the table of contents. pg_restore reads a custom-format
// archive's table of contents from a pipe and exits without reading the data
// that follows; the tee then drops its copy and the upload continues. When
// the piped check fails, the check reads the stored object back instead.
func (s *Service) dump(ctx context.Context, t target, tools Tools, snapshot string, excluded []string, key string) (ObjectEntry, string, error) {
	conn, env, err := libpqTarget(s.opts.DatabaseURL, "")
	if err != nil {
		return ObjectEntry{}, "", err
	}
	args := []string{"--format=custom", "--compress=zstd:3", "--no-password", "--snapshot=" + snapshot}
	for _, table := range excluded {
		args = append(args, "--exclude-table-data-and-children=public."+table)
	}
	args = append(args, "--dbname="+conn)
	cmd := exec.CommandContext(ctx, tools.PgDump, args...) //nolint:gosec // G204: the discovered pg_dump with arguments built here, no shell
	cmd.Env = append(os.Environ(), env...)
	stderr := &limitedBuffer{limit: stderrLimit}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ObjectEntry{}, "", err
	}

	check := exec.CommandContext(ctx, tools.PgRestore, "--list") //nolint:gosec // G204: the discovered pg_restore, no shell
	checkOut := &limitedBuffer{limit: 1 << 20}
	checkErr := &limitedBuffer{limit: stderrLimit}
	check.Stdout, check.Stderr = checkOut, checkErr
	checkIn, err := check.StdinPipe()
	if err != nil {
		return ObjectEntry{}, "", err
	}
	if err := cmd.Start(); err != nil {
		return ObjectEntry{}, "", fmt.Errorf("starting pg_dump: %w", err)
	}
	tee := &dropOnErrorWriter{w: checkIn}
	if err := check.Start(); err != nil {
		tee.drop()
	}
	entry, putErr := t.put(ctx, key, io.TeeReader(stdout, tee))
	if putErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	_ = checkIn.Close()
	piped := check.Process != nil && check.Wait() == nil && strings.TrimSpace(checkOut.String()) != ""
	switch {
	case putErr != nil:
		return ObjectEntry{}, "", fmt.Errorf("writing db.dump: %w", putErr)
	case waitErr != nil:
		return ObjectEntry{}, "", fmt.Errorf("pg_dump failed: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	if piped {
		return entry, tocCheckPipe, nil
	}
	slog.InfoContext(ctx, "piped table of contents check failed; reading the dump back", "component", "backup", "stderr", strings.TrimSpace(checkErr.String()))
	if err := readBackTOC(ctx, t, tools, key); err != nil {
		return ObjectEntry{}, "", err
	}
	return entry, tocCheckReadBack, nil
}

func readBackTOC(ctx context.Context, t target, tools Tools, key string) error {
	body, _, err := t.Open(ctx, key)
	if err != nil {
		return fmt.Errorf("reading db.dump back: %w", err)
	}
	defer func() { _ = body.Close() }()
	return listTOC(ctx, tools, body)
}

// listTOC runs `pg_restore --list` over an archive and fails unless it prints
// a table of contents.
func listTOC(ctx context.Context, tools Tools, archive io.Reader) error {
	cmd := exec.CommandContext(ctx, tools.PgRestore, "--list") //nolint:gosec // G204: the discovered pg_restore, no shell
	out := &limitedBuffer{limit: 1 << 20}
	errOut := &limitedBuffer{limit: stderrLimit}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = archive, out, errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the dump's table of contents is unreadable: %w: %s", err, strings.TrimSpace(errOut.String()))
	}
	if strings.TrimSpace(out.String()) == "" {
		return errors.New("the dump's table of contents is empty")
	}
	return nil
}

// dropOnErrorWriter copies to w until a write fails, then discards: the
// table of contents check may stop reading long before the dump ends, and
// that must never stall or fail the upload.
type dropOnErrorWriter struct {
	mu      sync.Mutex
	w       io.Writer
	dropped bool
}

func (d *dropOnErrorWriter) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.dropped {
		if _, err := d.w.Write(p); err != nil {
			d.dropped = true
		}
	}
	return len(p), nil
}

func (d *dropOnErrorWriter) drop() {
	d.mu.Lock()
	d.dropped = true
	d.mu.Unlock()
}

// limitedBuffer keeps the first limit bytes written and discards the rest.
type limitedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
