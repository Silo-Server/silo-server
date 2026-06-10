package jellycompat

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Silo-Server/silo-server/internal/config"
)

func newCompatWebFSFromDirectory(root string) (fs.FS, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}

	webFS := os.DirFS(root)
	if _, err := fs.Stat(webFS, "index.html"); err != nil {
		return nil, err
	}
	return webFS, nil
}

func newCompatWebHandler(webFS fs.FS, version string) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		cleanPath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		relPath := strings.TrimPrefix(cleanPath, "/")
		switch relPath {
		case "", ".":
			relPath = "index.html"
		}

		if version != "" {
			w.Header().Set("X-Silo-Jellyfin-Web-Version", version)
		}
		// Block content sniffing on everything we serve here. A CSP is
		// intentionally NOT set: the vendored jellyfin-web bundle relies on
		// inline scripts and would break under any meaningful policy.
		w.Header().Set("X-Content-Type-Options", "nosniff")

		if fileExists(webFS, relPath) {
			if relPath == "index.html" {
				indexBytes, err := fs.ReadFile(webFS, "index.html")
				if err != nil {
					http.Error(w, "index.html not found", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = w.Write(indexBytes)
				}
				return
			}

			if info, err := fs.Stat(webFS, relPath); err == nil && info.IsDir() && !strings.HasSuffix(r.URL.Path, "/") {
				target := path.Clean("/web/" + relPath)
				http.Redirect(w, r, target+"/", http.StatusMovedPermanently)
				return
			}

			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			req := r.Clone(r.Context())
			req.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, "/")
			fileServer.ServeHTTP(w, req)
			return
		}

		if shouldServeCompatWebIndex(relPath) {
			indexBytes, err := fs.ReadFile(webFS, "index.html")
			if err != nil {
				http.Error(w, "index.html not found", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(indexBytes)
			}
			return
		}

		http.NotFound(w, r)
	})
}

func shouldServeCompatWebIndex(relPath string) bool {
	if relPath == "" || relPath == "." || relPath == "index.html" {
		return true
	}
	base := path.Base(relPath)
	return !strings.Contains(base, ".")
}

func fileExists(fsys fs.FS, name string) bool {
	if _, err := fs.Stat(fsys, name); err == nil {
		return true
	}
	return false
}

func resolveCompatWebFS(deps Dependencies) (fs.FS, error) {
	if deps.WebFS != nil {
		if _, err := fs.Stat(deps.WebFS, "index.html"); err != nil {
			return nil, err
		}
		return deps.WebFS, nil
	}
	if deps.Config == nil {
		return nil, nil
	}

	for _, root := range compatWebSearchRoots(deps.Config.JellyfinCompat.WebDir) {
		webFS, err := newCompatWebFSFromDirectory(root)
		if err == nil {
			return webFS, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		return nil, err
	}
	return nil, fs.ErrNotExist
}

func compatWebVersion(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.JellyfinCompat.WebVersion
}

func compatWebSearchRoots(configuredRoot string) []string {
	roots := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(configuredRoot); trimmed != "" {
		roots = append(roots, trimmed)
	}
	if trimmed := strings.TrimSpace(vendoredCompatWebDir()); trimmed != "" && trimmed != strings.TrimSpace(configuredRoot) {
		roots = append(roots, trimmed)
	}
	return roots
}

func vendoredCompatWebDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	return filepath.Join(repoRoot, "third_party", "jellyfin-web", "current")
}
