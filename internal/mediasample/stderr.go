package mediasample

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

const (
	// maxStderrLine is the longest ffmpeg log line the router reads whole.
	// Filter output such as metadata=print can run long; a longer line is
	// dropped and parsing resumes at the next line.
	maxStderrLine = 1 << 20
	// stderrTailBytes bounds the log kept for error reports.
	stderrTailBytes = 4 << 10
	// errorLineBytes bounds the stderr line an Error message quotes.
	errorLineBytes = 256
)

// stderrRouter feeds each ffmpeg log line to the output parsers and keeps a
// bounded tail for error reports.
type stderrRouter struct {
	handlers []func(line string)
	tail     []string
	tailSize int
}

func newStderrRouter(handlers ...func(line string)) *stderrRouter {
	return &stderrRouter{handlers: handlers}
}

// start returns the writer to hand ffmpeg as stderr and a function that waits
// until every written line was routed. Close the writer once the process has
// exited, then call wait.
func (r *stderrRouter) start() (io.WriteCloser, func()) {
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64<<10), maxStderrLine)
		scanner.Split(newLogLineSplitter(maxStderrLine))
		for scanner.Scan() {
			r.route(scanner.Text())
		}
		// The splitter never fails, so the scan only ends at EOF or on a read
		// error. Keep draining so ffmpeg never blocks writing to a pipe nobody
		// reads.
		_, _ = io.Copy(io.Discard, reader)
	}()
	return writer, func() { <-done }
}

// newLogLineSplitter splits ffmpeg's log at "\n" and at "\r". ffmpeg ends
// its progress line with a bare "\r", so without the second split a long
// run's progress would pile up into one line in front of the next log message.
// A line of maxLine bytes or more is dropped instead of failing the scan, so
// one oversized metadata line cannot silently end parsing for the whole run.
// The scanner's buffer limit must be maxLine.
func newLogLineSplitter(maxLine int) bufio.SplitFunc {
	skipping := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
			if skipping {
				skipping = false
				return i + 1, nil, nil
			}
			return i + 1, data[:i], nil
		}
		if len(data) >= maxLine {
			skipping = true
			return len(data), nil, nil
		}
		if atEOF && len(data) > 0 {
			if skipping {
				return len(data), nil, nil
			}
			return len(data), data, nil
		}
		return 0, nil, nil
	}
}

func (r *stderrRouter) route(line string) {
	if line == "" {
		return
	}
	for _, handle := range r.handlers {
		handle(line)
	}
	r.keep(line)
}

// keep appends a line to the tail, dropping the oldest lines past the bound.
func (r *stderrRouter) keep(line string) {
	if len(line) > stderrTailBytes {
		line = line[len(line)-stderrTailBytes:]
	}
	r.tail = append(r.tail, line)
	r.tailSize += len(line)
	for r.tailSize > stderrTailBytes && len(r.tail) > 1 {
		r.tailSize -= len(r.tail[0])
		r.tail = r.tail[1:]
	}
}

// Tail returns the last lines of the log, oldest first.
func (r *stderrRouter) Tail() string {
	return strings.Join(r.tail, "\n")
}

// lastLine returns the last non-empty log line, shortened for an error message.
// Callers store error messages in Postgres text columns, which reject invalid
// UTF-8 and NUL bytes, and ffmpeg echoes file names byte for byte.
func lastLine(tail string) string {
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		truncated := len(line) > errorLineBytes
		if truncated {
			line = line[:errorLineBytes]
		}
		line = strings.ToValidUTF8(strings.ReplaceAll(line, "\x00", ""), "")
		if truncated {
			line += "…"
		}
		return line
	}
	return ""
}
