package activitylog

// captureWriter records every entry the middleware emits.
type captureWriter struct {
	entries []LogEntry
}

func (w *captureWriter) Write(entry LogEntry) { w.entries = append(w.entries, entry) }
func (w *captureWriter) Close() error         { return nil }
