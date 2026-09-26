package executor

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

const capturedCursorQuery = "cursor"

// Apply only after fixture expansion, directly to the constructed request.
// Captured bytes are values, never templates or an alternate destination URL.
func applyResponseBindings(req *http.Request, previous *response, bindings []scenariocatalog.ResponseBinding, seenCursors map[string]bool, trackCursor bool) error {
	if len(bindings) == 0 {
		return nil
	}
	if previous == nil {
		return fmt.Errorf("response capture has no previous exchange")
	}
	for i, b := range bindings {
		var value any
		if b.Header != "" {
			values := previous.Headers.Values(b.Header)
			if len(values) != 1 {
				return fmt.Errorf("capture[%d]: response header missing or ambiguous", i)
			}
			value = values[0]
		} else if b.Pointer != nil {
			var found bool
			value, found = resolvePointer(previous.Doc, *b.Pointer)
			if !previous.IsJSON || !found {
				return fmt.Errorf("capture[%d]: response JSON pointer missing", i)
			}
		} else {
			return fmt.Errorf("capture[%d]: missing source", i)
		}
		text, ok := value.(string)
		if !ok || text == "" || len(text) > scenariocatalog.MaxCapturedStringBytes {
			return fmt.Errorf("capture[%d]: expected nonempty bounded string", i)
		}
		if b.Query != "" {
			if b.Query == capturedCursorQuery && trackCursor {
				if seenCursors[text] {
					return fmt.Errorf("capture[%d]: repeated continuation cursor", i)
				}
				seenCursors[text] = true
			}
			q := req.URL.Query()
			q.Set(b.Query, text)
			req.URL.RawQuery = q.Encode()
		} else {
			if strings.ContainsAny(text, "\r\n\x00") {
				return fmt.Errorf("capture[%d]: invalid header value", i)
			}
			req.Header.Set(b.RequestHeader, text)
		}
	}
	return nil
}
