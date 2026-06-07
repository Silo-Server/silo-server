package jellycompat

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
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

func newDynamicCompatWebHandler(deps Dependencies) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webFS, version, err := resolveCompatWebFS(r.Context(), deps)
		if err != nil || webFS == nil {
			http.Error(w, "Jellyfin Web UI assets are not installed", http.StatusNotFound)
			return
		}
		newCompatWebHandler(webFS, version).ServeHTTP(w, r)
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

func resolveCompatWebFS(ctx context.Context, deps Dependencies) (fs.FS, string, error) {
	if deps.WebFS != nil {
		if _, err := fs.Stat(deps.WebFS, "index.html"); err != nil {
			return nil, "", err
		}
		return deps.WebFS, compatWebVersion(ctx, deps), nil
	}
	if deps.Config == nil {
		return nil, "", nil
	}

	root := compatWebDir(ctx, deps)
	if root == "" {
		return nil, "", nil
	}
	webFS, err := newCompatWebFSFromDirectory(root)
	if err != nil {
		return nil, "", err
	}
	return webFS, compatWebVersion(ctx, deps), nil
}

func compatWebDir(ctx context.Context, deps Dependencies) string {
	if deps.SettingsRepo != nil {
		if value, _ := deps.SettingsRepo.Get(ctx, "jellyfin_compat.web_dir"); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if deps.Config == nil {
		return ""
	}
	return strings.TrimSpace(deps.Config.JellyfinCompat.WebDir)
}

func compatWebVersion(ctx context.Context, deps Dependencies) string {
	if deps.SettingsRepo != nil {
		if value, _ := deps.SettingsRepo.Get(ctx, "jellyfin_compat.web_version"); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if deps.Config == nil {
		return ""
	}
	return deps.Config.JellyfinCompat.WebVersion
}
