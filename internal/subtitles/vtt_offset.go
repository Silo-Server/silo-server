package subtitles

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

// WebVTTOffsetWriter shifts source-timed WebVTT onto a receiver's video clock.
// It buffers at most one cue block, preserving cue IDs, settings, and markup.
// Whole-track extraction caches stay in original-media time.
type WebVTTOffsetWriter struct {
	output     io.Writer
	offset     time.Duration
	pending    []byte
	block      []string
	blockBytes int
	err        error
}

func NewWebVTTOffsetWriter(output io.Writer, offset time.Duration) *WebVTTOffsetWriter {
	return &WebVTTOffsetWriter{output: output, offset: offset}
}

const maxWebVTTBlockBytes = 1 << 20

func (w *WebVTTOffsetWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	consumed := 0
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			end = len(p)
		} else {
			end++
		}
		if w.blockBytes+len(w.pending)+end > maxWebVTTBlockBytes {
			w.err = errors.New("WebVTT cue block exceeds supported size")
			return consumed, w.err
		}
		w.pending = append(w.pending, p[:end]...)
		p = p[end:]
		consumed += end
		if w.pending[len(w.pending)-1] == '\n' {
			line := strings.TrimSuffix(strings.TrimSuffix(string(w.pending), "\n"), "\r")
			w.pending = w.pending[:0]
			if line == "" {
				w.err = w.flushBlock()
			} else {
				w.block = append(w.block, line)
				w.blockBytes += len(line)
			}
			if w.err != nil {
				return consumed, w.err
			}
		}
	}
	return consumed, nil
}

func (w *WebVTTOffsetWriter) Close() error {
	if w.err != nil {
		return w.err
	}
	if len(w.pending) > 0 {
		w.block = append(w.block, strings.TrimSuffix(string(w.pending), "\r"))
		w.pending = nil
	}
	w.err = w.flushBlock()
	return w.err
}

var inlineWebVTTTimestamp = regexp.MustCompile(`<((?:[0-9]+:)?[0-9]{2}:[0-9]{2}\.[0-9]{3})>`)

func (w *WebVTTOffsetWriter) flushBlock() error {
	lines := w.block
	w.block = nil
	w.blockBytes = 0
	if len(lines) == 0 {
		return nil
	}
	// A cue has an optional identifier followed immediately by its timing line.
	// NOTE/STYLE/REGION blocks may contain arrow-like text that is not a cue.
	first := strings.TrimPrefix(lines[0], "\ufeff")
	metadata := strings.HasPrefix(first, "WEBVTT") || first == "NOTE" || strings.HasPrefix(first, "NOTE ") || strings.HasPrefix(first, "NOTE\t") || first == "STYLE" || first == "REGION"
	timing := -1
	if !metadata {
		for i := 0; i < len(lines) && i < 2; i++ {
			if strings.Contains(lines[i], "-->") {
				timing = i
				break
			}
		}
	}
	if timing >= 0 {
		start, end, err := parseTimingLine(lines[timing])
		if err != nil {
			return fmt.Errorf("shift WebVTT timing: %w", err)
		}
		start += w.offset
		end += w.offset
		if end <= 0 {
			return nil
		}
		if start < 0 {
			start = 0
		}
		after := strings.TrimSpace(strings.SplitN(lines[timing], "-->", 2)[1])
		settings := ""
		if i := strings.IndexAny(after, " \t"); i >= 0 {
			settings = after[i:]
		}
		lines[timing] = formatWebVTTTimestamp(start) + " --> " + formatWebVTTTimestamp(end) + settings
		for i := timing + 1; i < len(lines); i++ {
			lines[i] = inlineWebVTTTimestamp.ReplaceAllStringFunc(lines[i], func(tag string) string {
				timestamp, err := parseTimestamp(tag[1 : len(tag)-1])
				if err != nil {
					return tag
				}
				timestamp += w.offset
				// Inline timestamps outside the surviving cue interval cannot be active.
				if timestamp <= start || timestamp >= end {
					return ""
				}
				return "<" + formatWebVTTTimestamp(timestamp) + ">"
			})
		}
	} else if strings.HasPrefix(first, "WEBVTT") {
		lines = slices.DeleteFunc(lines, func(line string) bool {
			return strings.HasPrefix(line, "X-TIMESTAMP-MAP=")
		})
	}
	_, err := io.WriteString(w.output, strings.Join(lines, "\n")+"\n\n")
	return err
}

func formatWebVTTTimestamp(timestamp time.Duration) string {
	return strings.Replace(formatSRTTimestamp(timestamp), ",", ".", 1)
}
