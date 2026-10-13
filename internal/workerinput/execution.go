package workerinput

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type executionKey struct{}

const admittedMediaFormats = "matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,avi,mpegts,mpeg,mpegvideo,hevc,h264,flac,ogg,mp3,aac,wav,asf,flv"

// WithContext carries the admitted input registry through probes and restarts.
func WithContext(ctx context.Context, adapter *Adapter) context.Context {
	return context.WithValue(ctx, executionKey{}, adapter)
}

// Lookup returns only a live registered input. It never accepts an arbitrary
// network URL based on its spelling.
func Lookup(ctx context.Context, path string) (*Handle, bool) {
	a, ok := ctx.Value(executionKey{}).(*Adapter)
	if !ok {
		return nil, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, h := range a.handles {
		if h.URL() == path && a.authorized(h) {
			return h, true
		}
	}
	return nil, false
}

func IsStandalone(ctx context.Context) bool { _, ok := ctx.Value(executionKey{}).(*Adapter); return ok }

// ValidateOutput permits existing and not-yet-created paths only within the
// configured worker scratch. Every existing ancestor is resolved first, so an
// escaping or dangling symlink cannot become an owned destination.
func ValidateOutput(ctx context.Context, path string) error {
	if !IsStandalone(ctx) {
		return nil
	}
	denied := errors.New("worker output is outside configured scratch")
	a, _ := ctx.Value(executionKey{}).(*Adapter)
	if a == nil || a.options.LocalRoot == "" || !filepath.IsAbs(path) {
		return denied
	}
	root, err := filepath.EvalSymlinks(a.options.LocalRoot)
	if err != nil {
		return denied
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return denied
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return denied
	}
	ancestor := filepath.Clean(path)
	var missing []string
	var resolved string
	for {
		resolved, err = filepath.EvalSymlinks(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return denied
		}
		if _, statErr := os.Lstat(ancestor); !errors.Is(statErr, os.ErrNotExist) {
			// EvalSymlinks can return ENOENT for an existing dangling link.
			return denied
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return denied
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return denied
	}
	return nil
}

// Validate requires admitted authority in standalone mode; local execution
// retains its existing catalog/filesystem validation.
func Validate(ctx context.Context, path string) error {
	if !IsStandalone(ctx) {
		return nil
	}
	if _, ok := Lookup(ctx, path); !ok {
		a, _ := ctx.Value(executionKey{}).(*Adapter)
		root, rootErr := filepath.EvalSymlinks(a.options.LocalRoot)
		target, err := filepath.EvalSymlinks(path)
		if a.options.LocalRoot == "" || rootErr != nil || err != nil {
			return errors.New("executor input is outside current source authority")
		}
		relative, err := filepath.Rel(root, target)
		info, statErr := os.Stat(target)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || statErr != nil || !info.Mode().IsRegular() {
			return errors.New("executor input is outside worker scratch")
		}
	}
	return nil
}

// RestrictArgs applies per-input protocol and demuxer limits. Container
// playlists, concat scripts and image manifests cannot open additional URLs or
// local files through the admitted media representation.
func RestrictArgs(ctx context.Context, args []string) ([]string, error) {
	if !IsStandalone(ctx) {
		return args, nil
	}
	result := make([]string, 0, len(args)+8)
	for i := 0; i < len(args); i++ {
		if args[i] == "-i" && i+1 < len(args) {
			input := args[i+1]
			if input != "-" && input != "pipe:0" {
				if err := Validate(ctx, input); err != nil {
					return nil, err
				}
				if _, remote := Lookup(ctx, input); remote {
					result = append(result, "-protocol_whitelist", "http,tcp", "-format_whitelist", admittedMediaFormats)
				}
			}
		}
		result = append(result, args[i])
	}
	return result, nil
}

// CommandContext applies source restrictions before process creation. A refused
// invocation returns a canceled command, so callers retain normal Start/Output
// error handling and no subprocess can open the rejected input.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	probe := strings.Contains(filepath.Base(name), "ffprobe")
	restricted, err := RestrictArgs(ctx, args)
	// Every media probe in Silo places its positional input last. Validate it
	// even when it is not a registered URL, before allowing process creation.
	if err == nil && IsStandalone(ctx) && probe && len(args) > 0 {
		input := args[len(args)-1]
		if !strings.HasPrefix(input, "-") && !containsInputOption(args, input) {
			err = Validate(ctx, input)
		}
	}
	if IsStandalone(ctx) && probe {
		adapter, _ := ctx.Value(executionKey{}).(*Adapter)
		if adapter.options.FFprobe != "" {
			name = adapter.options.FFprobe
		}
	}
	if err != nil {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return exec.CommandContext(canceled, name, args...)
	}
	// FFprobe accepts a positional input rather than -i.
	for _, arg := range args {
		if _, ok := Lookup(ctx, arg); ok && !containsInputOption(args, arg) {
			restricted = append([]string{"-protocol_whitelist", "http,tcp", "-format_whitelist", admittedMediaFormats}, restricted...)
			break
		}
	}
	return exec.CommandContext(ctx, name, restricted...)
}
func containsInputOption(args []string, value string) bool {
	for i := 1; i < len(args); i++ {
		if args[i-1] == "-i" && args[i] == value {
			return true
		}
	}
	return false
}

func StableIdentity(ctx context.Context, path string) string {
	if h, ok := Lookup(ctx, path); ok {
		return h.Identity()
	}
	return strings.TrimSpace(path)
}
