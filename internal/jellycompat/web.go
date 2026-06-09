package jellycompat

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
)

func newCompatWebFSFromDirectory(root string) (fs.FS, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}

	if _, err := validateWebComponentDirectory(root); err != nil {
		return nil, err
	}
	webFS := os.DirFS(root)
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
		ctx := r.Context()
		webFS, version, err := resolveCompatWebFS(ctx, deps)
		if err != nil || webFS == nil {
			if err == nil && !compatWebEnabled(ctx, deps) && compatWebAssetsInstalled(ctx, deps) {
				http.Error(w, compatWebDisabledMessage(ctx, deps), http.StatusNotFound)
				return
			}
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
	if !compatWebEnabled(ctx, deps) {
		return nil, "", nil
	}
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

func compatWebEnabled(ctx context.Context, deps Dependencies) bool {
	proxyEnabled, webEnabled := compatWebEnablement(ctx, deps)
	return proxyEnabled && webEnabled
}

func compatWebDisabledMessage(ctx context.Context, deps Dependencies) string {
	proxyEnabled, webEnabled := compatWebEnablement(ctx, deps)
	switch {
	case !proxyEnabled:
		return "Jellyfin Web UI is disabled because the Jellyfin compatibility proxy is disabled"
	case !webEnabled:
		return "Jellyfin Web UI is disabled in Silo settings"
	default:
		return "Jellyfin Web UI is disabled"
	}
}

func compatWebEnablement(ctx context.Context, deps Dependencies) (bool, bool) {
	proxyEnabled := deps.Config == nil || deps.Config.JellyfinCompat.Enabled
	webEnabled := true
	if deps.Config != nil {
		webEnabled = deps.Config.JellyfinCompat.WebEnabled
	}
	if deps.SettingsRepo != nil {
		if value, _ := deps.SettingsRepo.Get(ctx, "jellyfin_compat.enabled"); strings.TrimSpace(value) != "" {
			if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
				proxyEnabled = parsed
			}
		}
		if value, _ := deps.SettingsRepo.Get(ctx, "jellyfin_compat.web_enabled"); strings.TrimSpace(value) != "" {
			if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
				webEnabled = parsed
			}
		}
	}
	return proxyEnabled, webEnabled
}

func compatWebAssetsInstalled(ctx context.Context, deps Dependencies) bool {
	if deps.WebFS != nil {
		_, err := fs.Stat(deps.WebFS, "index.html")
		return err == nil
	}
	if deps.Config == nil {
		return false
	}
	root := compatWebDir(ctx, deps)
	if root == "" {
		return false
	}
	_, err := validateWebComponentDirectory(root)
	return err == nil
}

func compatWebDir(ctx context.Context, deps Dependencies) string {
	root := ""
	if deps.SettingsRepo != nil {
		if value, _ := deps.SettingsRepo.Get(ctx, "jellyfin_compat.web_install_dir"); strings.TrimSpace(value) != "" {
			root = strings.TrimSpace(value)
		}
	}
	if root == "" {
		if deps.Config == nil {
			return ""
		}
		root = DefaultWebInstallRoot(deps.Config)
	}
	root, err := normalizeWebInstallRoot(root)
	if err != nil {
		return ""
	}
	return ManagedWebInstallPath(root)
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
