package downloads

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// VirtualInputResolver resolves a provider-backed catalog row to a concrete,
// in-process input. See node-aware download preparation in the fork's
// virtual_delivery.go.
type VirtualInputResolver interface {
	ResolveVirtualDownloadInput(ctx context.Context, file *models.MediaFile) (inputPath string, cleanup func(), err error)
}

// isVirtualMediaFile reports a zero-storage catalog row backed by a provider
// URI (`virtual://…`) or marked with the virtual container. Such a row has no
// local bytes for a direct download to serve.
func isVirtualMediaFile(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(file.Container), "virtual") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.FilePath)), "virtual://")
}

// isLoopbackPrepareInput reports whether a prepare input is a URL that only
// this host can open: an http(s) URL whose host is localhost or a loopback IP.
func isLoopbackPrepareInput(inputPath string) bool {
	parsed, err := url.Parse(strings.TrimSpace(inputPath))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Fork v2 direct-download sentinels. They wrap catalog.ErrItemNotFound so
// not-found callers still match, while the v2 adapter can name the distinct
// refusal.
var (
	ErrFileUnavailable  = errors.New("requested media file is no longer available")
	ErrFileAccessDenied = errors.New("requested media file is not accessible to this profile")
)

// SetVirtualInputResolver wires provider-backed download resolution into the
// artifact manager.
func (m *ArtifactManager) SetVirtualInputResolver(resolver VirtualInputResolver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.virtualInput = resolver
	m.mu.Unlock()
	if resolver != nil {
		m.triggerDrain()
	}
}

// CancelAbandonedPrepare releases an artifact job whose last download row was
// just deleted. RetireAbandonedPreparations re-checks the links inside the
// deleting statement, so a request that attached since the caller looked keeps
// the job, and a genuinely abandoned job stops this replica's attempt at once.
func (m *ArtifactManager) CancelAbandonedPrepare(ctx context.Context, artifactID string) {
	if m == nil || artifactID == "" || m.repo == nil {
		return
	}
	retired, err := m.repo.RetireAbandonedPreparations(ctx, []string{artifactID})
	if err != nil {
		slog.WarnContext(ctx, "releasing an abandoned download preparation failed", "component", "downloads", "artifact_id", artifactID, "error", err)
		return
	}
	m.announceRetired(ctx, retired)
}
