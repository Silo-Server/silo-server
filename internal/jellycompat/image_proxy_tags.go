package jellycompat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

func compatImageProxyTagVariantMiddleware(codec *ResourceIDCodec) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || !isCompatImageProxyClientRequest(r) || isWebSocketUpgrade(r) {
				next.ServeHTTP(w, r)
				return
			}

			rw := &compatImageProxyTagResponseWriter{ResponseWriter: w, codec: codec}
			next.ServeHTTP(rw, r)
			rw.finish()
		})
	}
}

type compatImageProxyTagResponseWriter struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	passthrough bool
	codec       *ResourceIDCodec
}

func (w *compatImageProxyTagResponseWriter) WriteHeader(status int) {
	if w.passthrough {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
}

func (w *compatImageProxyTagResponseWriter) Write(p []byte) (int, error) {
	if w.passthrough {
		return w.ResponseWriter.Write(p)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if !isJSONResponse(w.Header().Get("Content-Type")) {
		w.passthrough = true
		w.ResponseWriter.WriteHeader(w.status)
		return w.ResponseWriter.Write(p)
	}
	return w.body.Write(p)
}

// Flush implements http.Flusher for the passthrough (non-JSON) path only.
// While buffering a JSON body for tag rewriting there is nothing downstream
// to flush, and flushing the inner writer would commit headers before
// finish() has rewritten the response.
func (w *compatImageProxyTagResponseWriter) Flush() {
	if !w.passthrough {
		contentType := w.Header().Get("Content-Type")
		if contentType == "" || isJSONResponse(contentType) {
			return
		}
		w.passthrough = true
		status := w.status
		if status == 0 {
			status = http.StatusOK
		}
		w.ResponseWriter.WriteHeader(status)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *compatImageProxyTagResponseWriter) finish() {
	if w.passthrough {
		return
	}
	if w.status == 0 && w.body.Len() == 0 {
		return
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	body := w.body.Bytes()
	if isJSONResponse(w.Header().Get("Content-Type")) && len(body) > 0 {
		body = rewriteCompatImageProxyTags(w.codec, body)
	}
	w.ResponseWriter.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.ResponseWriter.Write(body)
	}
}

func isJSONResponse(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return contentType == "application/json" || strings.HasPrefix(contentType, "application/json;")
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func rewriteCompatImageProxyTags(codec *ResourceIDCodec, body []byte) []byte {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return body
	}
	if !appendCompatImageProxyTags(codec, value) {
		return body
	}
	rewritten, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return append(rewritten, '\n')
}

func appendCompatImageProxyTags(codec *ResourceIDCodec, value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		hasPrimaryImageTag := compatMapHasPrimaryImageTag(typed)
		for key, child := range typed {
			switch key {
			case "ImageTags":
				changed = appendCompatImageProxyTagsInMap(child) || changed
			case "ImageBlurHashes":
				changed = appendCompatImageProxyTagsInBlurHashes(child) || changed
			case "BackdropImageTags", "ParentBackdropImageTags":
				changed = appendCompatImageProxyTagsInSlice(child) || changed
			case "PrimaryImageTag", "SeriesPrimaryImageTag", "ParentThumbImageTag", "BackdropImageTag", "ImageTag":
				if tag, ok := child.(string); ok && tag != "" {
					typed[key] = compatImageProxyTag(tag)
					changed = true
				}
			default:
				changed = appendCompatImageProxyTags(codec, child) || changed
			}
		}
		if hasPrimaryImageTag {
			changed = appendCompatPrimaryImageItemID(codec, typed) || changed
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			changed = appendCompatImageProxyTags(codec, child) || changed
		}
		return changed
	default:
		return false
	}
}

func compatMapHasPrimaryImageTag(value map[string]any) bool {
	if tags, ok := value["ImageTags"].(map[string]any); ok {
		if tag, ok := tags["Primary"].(string); ok && strings.TrimSpace(tag) != "" {
			return true
		}
	}
	for _, key := range []string{"PrimaryImageTag", "ImageTag"} {
		if tag, ok := value[key].(string); ok && strings.TrimSpace(tag) != "" {
			return true
		}
	}
	return false
}

func appendCompatPrimaryImageItemID(codec *ResourceIDCodec, value map[string]any) bool {
	if codec == nil {
		return false
	}
	routeID := compatStringMapValue(value, "PrimaryImageItemId")
	if routeID == "" {
		routeID = compatStringMapValue(value, "Id")
	}
	if routeID == "" {
		routeID = compatStringMapValue(value, "ItemId")
	}
	if routeID == "" {
		return false
	}
	proxyRouteID := compatImageProxyRouteID(codec, routeID)
	if proxyRouteID == "" || proxyRouteID == routeID {
		return false
	}
	if compatStringMapValue(value, "PrimaryImageItemId") == proxyRouteID {
		return false
	}
	value["PrimaryImageItemId"] = proxyRouteID
	return true
}

func compatStringMapValue(value map[string]any, key string) string {
	if raw, ok := value[key].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func appendCompatImageProxyTagsInMap(value any) bool {
	tags, ok := value.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for key, raw := range tags {
		if tag, ok := raw.(string); ok && tag != "" {
			tags[key] = compatImageProxyTag(tag)
			changed = true
		}
	}
	return changed
}

func appendCompatImageProxyTagsInSlice(value any) bool {
	tags, ok := value.([]any)
	if !ok {
		return false
	}
	changed := false
	for idx, raw := range tags {
		if tag, ok := raw.(string); ok && tag != "" {
			tags[idx] = compatImageProxyTag(tag)
			changed = true
		}
	}
	return changed
}

func appendCompatImageProxyTagsInBlurHashes(value any) bool {
	byImageType, ok := value.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for imageType, rawTags := range byImageType {
		tags, ok := rawTags.(map[string]any)
		if !ok {
			continue
		}
		next := make(map[string]any, len(tags))
		for tag, hash := range tags {
			next[compatImageProxyTag(tag)] = hash
			changed = true
		}
		byImageType[imageType] = next
	}
	return changed
}
